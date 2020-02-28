package main

import (
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/gorilla/mux"

	"github.com/coreos/go-systemd/daemon"
	"github.com/martins-home-automation/libs/mqtthelper"
)

type PlayerInfo struct {
	Name         string `json:"name"`
	PID          int    `json:"pid"`
	GID          int    `json:"gid"`
	Model        string `json:"model"`
	Version      string `json:"version"`
	Network      string `json:"network"`
	Lineout      int    `json:"lineout"`
	Control      string `json:"control"`
	SerialNumber string `json:"serial"`

	State    string `json:"state"`
	Position int    `json:"position"`
	Duration int    `json:"duration"`
	Level    int    `json:"level"`
	Mute     string `json:"mute"`

	Playback Playback `json:"playback"`

	Config PlayerConfig `json:"config"`

	LastUserAction time.Time `json:"last_user_action"`
}

type PlayState struct {
	State string `json:"state"`
}

var heos *HEOS
var config HeosConfig
var players []PlayerInfo
var sources SourceList

func getPlayerByPID(pid int) *PlayerInfo {
	for idx, item := range players {
		if item.PID == pid {
			return &players[idx]
		}
	}
	return nil
}

func GetPlayers(w http.ResponseWriter, r *http.Request) {
	json.NewEncoder(w).Encode(players)
}

func GetSources(w http.ResponseWriter, r *http.Request) {
	json.NewEncoder(w).Encode(sources)
}

func GetPlayer(w http.ResponseWriter, r *http.Request) {
	params := mux.Vars(r)
	pid, err := strconv.Atoi(params["pid"])
	if err == nil {
		p := getPlayerByPID(pid)
		json.NewEncoder(w).Encode(p)
	} else {
		json.NewEncoder(w).Encode(&Player{})
	}
}

func SetPlayState(w http.ResponseWriter, r *http.Request) {
	params := mux.Vars(r)
	pid, err := strconv.Atoi(params["pid"])
	if err == nil {
		p := getPlayerByPID(pid)

		if p != nil {

			decoder := json.NewDecoder(r.Body)
			var ps PlayState
			err := decoder.Decode(&ps)
			if err != nil {
				w.WriteHeader(http.StatusBadRequest)
				return
			}

			log.Printf("SetPlayState %v, %v", pid, ps.State)
			heos.SetPlayState(pid, ps.State)
			for i := 0; i < 50; i++ {
				p = getPlayerByPID(pid)
				if p != nil && ((p.State == "play") == (ps.State == "play")) {
					break
				}
				time.Sleep(time.Millisecond * 100)
			}

			var response PlayState
			response.State = p.State
			json.NewEncoder(w).Encode(&response)
			return
		}
	}
	w.WriteHeader(http.StatusNotFound)
}

func GetPlayState(w http.ResponseWriter, r *http.Request) {
	params := mux.Vars(r)
	var response PlayState
	pid, err := strconv.Atoi(params["pid"])
	if err == nil {
		p := getPlayerByPID(pid)
		if p != nil {
			log.Printf("GetPlayState %v", pid)
			response.State = p.State
			json.NewEncoder(w).Encode(&response)
			return
		}
	}

	w.WriteHeader(http.StatusNotFound)
	response.State = "Not Found"
	json.NewEncoder(w).Encode(&response)
}

func sleepTimer(heos *HEOS) {
	t := time.NewTicker(time.Second)
	defer t.Stop()
	for {
		<-t.C
		if heos.IsConnected() {
			for _, p := range players {
				if p.Config.SleepTimer != 0 && p.State == "play" {
					sleepTime := p.LastUserAction.Add(time.Minute * time.Duration(p.Config.SleepTimer))
					if sleepTime.Before(time.Now()) {
						level := p.Level
						for i := 10; i >= 0; i-- {
							heos.SetVolume(p.PID, level*i/10)
							time.Sleep(time.Second)
						}

						heos.SetPlayState(p.PID, "pause")
						time.Sleep(time.Second)
						heos.SetVolume(p.PID, level)
					}
				}
			}
		}
	}
}

func main() {
	config.read()

	heosURI := os.Getenv("HEOS_URI")
	if heosURI == "" {
		panic("No URI for the HEOS capable device found in environment variable HEOS_URI")
	}

	router := mux.NewRouter()

	router.HandleFunc("/api/source", GetSources).Methods("GET")
	router.HandleFunc("/api/player", GetPlayers).Methods("GET")
	router.HandleFunc("/api/player/{pid}", GetPlayer).Methods("GET")
	router.HandleFunc("/api/player/{pid}/play_state", GetPlayState).Methods("GET")
	router.HandleFunc("/api/player/{pid}/play_state", SetPlayState).Methods("POST")

	signalChannel := make(chan os.Signal, 1)

	heos = NewHEOS(heosURI)
	err := connect(heos)

	if err != nil {
		log.Printf("connect failed: %v", err)
		return
	}

	go func() {
		log.Fatal(http.ListenAndServe(":8000", router))
	}()

	// go func() {
	// 	t := time.NewTicker(2 * time.Second)
	// 	defer t.Stop()
	// 	for {
	// 		<-t.C
	// 		if !heos.IsConnected() {
	// 			log.Printf("Starting reconnect")
	// 			connect(heos)
	// 		}
	// 	}

	// }()

	go sleepTimer(heos)

	daemon.SdNotify(false, "READY=1")
	signal.Notify(signalChannel, syscall.SIGINT, syscall.SIGTERM)

	running := true
	for running {
		select {
		case event := <-heos.EventCh:
			log.Printf("Got event: %#v", event)
			switch event.Type {
			case "disconnected":
				disconnect(heos)
				running = false
			case "players_changed":
				if err := updatePlayers(heos); err != nil {
					disconnect(heos)
					mqtthelper.HandleError(err, "Players changed")
				}
			case "player_volume_changed":
				updatePlayerVolume(heos, event.Params.Get("pid"), event.Params.Get("level"), event.Params.Get("mute"))
			case "sources_changed":
				if err := updateSources(heos); err != nil {
					disconnect(heos)
					mqtthelper.HandleError(err, "Sources changed")
				}
			case "player_state_changed":
				updatePlaybackState(heos, event.Params.Get("pid"), event.Params.Get("state"))

			case "player_now_playing_changed":
				updatePlayback(heos, event.Params.Get("pid"))

			case "groups_changed":
			case "group_volume_changed":

			case "player_queue_changed":

			case "player_now_playing_progress":
				updatePlaybackProgress(heos, event.Params.Get("pid"), event.Params.Get("cur_pos"), event.Params.Get("duration"))
			default:
				log.Printf("Unhandled event: %#v", event)
			}

		case <-signalChannel:
			running = false
		}
	}

	disconnect(heos)
}

func connect(h *HEOS) error {
	if err := h.Connect(); err != nil {
		disconnect(h)
		return err
	}

	if err := h.RegisterForChangeEvents(false); err != nil {
		return err
	}

	if err := updatePlayers(h); err != nil {
		log.Printf("updatePlayers failed: %v", err)
		disconnect(h)
		return err
	}
	if err := updateSources(h); err != nil {
		log.Printf("updateSources failed: %v", err)
		disconnect(h)
		return err
	}

	if err := h.RegisterForChangeEvents(true); err != nil {
		log.Printf("RegisterForChangeEvents failed: %v", err)
		return err
	}

	return nil
}

func disconnect(h *HEOS) {
	if h.IsConnected() {
		h.RegisterForChangeEvents(false)
		h.Disconnect()
	}
}

func updatePlayers(h *HEOS) error {
	plist, err := h.GetPlayers()
	if err != nil {
		return err
	}
	if len(plist) == 0 {
		return errors.New("No players found, cannot continue")
	}

	// todo delta update
	players = nil
	for _, p := range plist {

		var pinfo = PlayerInfo{
			PID:            p.PID,
			GID:            p.GID,
			Name:           p.Name,
			Model:          p.Model,
			Control:        p.Control,
			Lineout:        p.Lineout,
			Network:        p.Network,
			SerialNumber:   p.SerialNumber,
			Version:        p.Version,
			Config:         PlayerConfig{SleepTimer: 0},
			LastUserAction: time.Now(),
		}
		if c, ok := config.Player[p.PID]; ok {
			pinfo.Config = c
		}

		players = append(players, pinfo)

		updatePlayer(h, p.PID)
	}
	return nil
}

func updateSources(h *HEOS) error {
	s, err := h.GetSources()
	if err != nil {
		return err
	}
	if len(s) == 0 {
		return errors.New("No sources found, cannot continue")
	}

	sources = s
	return nil
}

func updatePlayer(h *HEOS, pid int) error {
	if !h.IsConnected() {
		return nil
	}

	player := getPlayerByPID(pid)

	if player == nil {
		return nil
	}

	level, err := h.GetVolume(pid)
	if err != nil {
		return err
	}
	player.Level = level

	mute, err := h.GetMute(pid)
	if err != nil {
		return err
	}
	player.Mute = mute

	state, err := h.GetPlayState(pid)
	if err != nil {
		return err
	}
	player.State = state

	if state == "stop" {
		return nil
	}

	p, err := h.GetPlayback(pid)
	if err != nil {
		return err
	}
	player.Playback = p
	return nil
}

func updatePlaybackState(h *HEOS, pidstr, state string) {
	pid, _ := strconv.Atoi(pidstr)
	var player = getPlayerByPID(pid)
	if player != nil {
		player.State = state
		player.LastUserAction = time.Now()
	}
}

func updatePlayback(h *HEOS, pidstr string) {
	pid, _ := strconv.Atoi(pidstr)
	var player = getPlayerByPID(pid)
	if player != nil {
		p, err := h.GetPlayback(pid)
		if err != nil {
			return
		}
		player.Playback = p
	}
}

func updatePlaybackProgress(h *HEOS, pidstr, elapsed string, duration string) {
	pid, _ := strconv.Atoi(pidstr)

	e, err := strconv.Atoi(elapsed)
	if err != nil {
		return
	}
	d, err := strconv.Atoi(duration)
	if err != nil {
		return
	}

	var player = getPlayerByPID(pid)
	if player != nil {
		player.Position = e
		player.Duration = d
	}
}

func updatePlayerVolume(h *HEOS, pidstr, level, mute string) {
	log.Printf("PlayerVolumeChangedEvent %v, %v, %v", pidstr, level, mute)
	var pid, _ = strconv.Atoi(pidstr)
	var l, _ = strconv.Atoi(level)

	var player = getPlayerByPID(pid)
	if player != nil {
		player.Level = l
		player.Mute = mute
		player.LastUserAction = time.Now()
	}

	var playState, _ = h.GetPlayState(pid)
	log.Printf("Current PlayState: %v", playState)

	if mute == "on" {
		h.SetMute(pid, "off")
		if playState == "play" {
			h.SetPlayState(pid, "pause")
		} else {
			h.SetPlayState(pid, "play")
		}
	}
}
