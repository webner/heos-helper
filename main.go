package main

import (
	"errors"
	"fmt"
	"log"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	flags "github.com/jessevdk/go-flags"
	"github.com/martins-home-automation/libs/mediacenter"
	"github.com/martins-home-automation/libs/mqtthelper"

	"github.com/coreos/go-systemd/daemon"
	"github.com/eclipse/paho.mqtt.golang"
)

type Config struct {
	Topics struct {
		System        string `yaml:"system"`
		PlaybackState string `yaml:"playback_state"`
		Play          string `yaml:"play"`
		SystemState   string `yaml:"system_state"`
		State         string `yaml:"state"`
		Info          string `yaml:"info"`
	} `yaml:"topics"`
}

type Info struct {
	Model        string `json:"model"`
	Version      string `json:"version"`
	SerialNumber string `json:"serialnumber"`
}

var cfg = Config{}
var info = Info{}
var players []Player
var sources SourceList

var playback = &mediacenter.Playback{
	Source: "HEOS",
	State:  "stop",
}

func main() {
	var mqttClient mqtt.Client

	heosURI := os.Getenv("HEOS_URI")
	if heosURI == "" {
		panic("No URI for the HEOS capable device found in environment variable HEOS_URI")
	}
	mqttURI := os.Getenv("MQTT_URI")
	if mqttURI == "" {
		panic("No URI for MQTT found in environment variable MQTT_URI")
	}

	if err := mqtthelper.ParseConfigOption(&cfg); err != nil {
		if e, ok := err.(*flags.Error); ok && e.Type == flags.ErrHelp {
			return
		}
		os.Exit(1)
	}

	messageChannel := mqtthelper.NewMessageChannel()
	signalChannel := make(chan os.Signal, 1)

	heos := NewHEOS(heosURI)
	defer heos.Disconnect()

	mqttClient, err := mqtthelper.NewClient(mqttURI, func(c mqtt.Client) {
		mqtthelper.HandleFatalError(mqtthelper.Subscribe(c, cfg.Topics.System, messageChannel))
		mqtthelper.HandleFatalError(mqtthelper.Subscribe(c, cfg.Topics.PlaybackState, messageChannel))
		mqtthelper.HandleFatalError(mqtthelper.Subscribe(c, fmt.Sprintf("%s/+", cfg.Topics.Play), messageChannel))

		log.Println("Started agent")

		// Try to connect on start
		mqtthelper.HandleError(connect(c, heos), "Connect")
	})
	if err != nil {
		log.Fatalf("Could not connect to MQTT at %s: %s", mqttURI, err)
	}
	defer mqttClient.Disconnect(100)

	daemon.SdNotify(false, "READY=1")
	signal.Notify(signalChannel, syscall.SIGINT, syscall.SIGTERM)

	running := true
	for running {
		select {
		case msg := <-messageChannel:
			switch msg.Topic() {
			case cfg.Topics.System:
				setSystemState(mqttClient, heos, msg.Payload())
			case cfg.Topics.PlaybackState:
				setPlaybackState(heos, msg.Payload())
			default:
				if strings.Index(msg.Topic(), cfg.Topics.Play) == 0 {
					play(heos, msg.Topic(), msg.Payload())
				}
			}

		case event := <-heos.EventCh:
			switch event.Type {
			case "disconnected":
				updatePlaybackState(mqttClient, "stop")
				disconnect(mqttClient, heos)
			case "players_changed":
				if err := updatePlayers(heos); err != nil {
					disconnect(mqttClient, heos)
					mqtthelper.HandleError(err, "Players changed")
				}
			case "sources_changed":
				if err := updateSources(heos); err != nil {
					disconnect(mqttClient, heos)
					mqtthelper.HandleError(err, "Sources changed")
				}
			case "player_state_changed":
				updatePlaybackState(mqttClient, event.Params.Get("state"))
			case "player_now_playing_changed":
				mqtthelper.HandleError(updatePlayback(mqttClient, heos), "Playback")
			case "player_now_playing_progress":
				updatePlaybackProgress(mqttClient, heos, event.Params.Get("cur_pos"), event.Params.Get("duration"))
			default:
				log.Printf("Unhandled event: %#v", event)
			}

		case <-signalChannel:
			running = false
		}
	}

	disconnect(mqttClient, heos)
}

func setSystemState(m mqtt.Client, h *HEOS, b []byte) {
	s, err := mqtthelper.ParseSetSystemState(b)
	if err != nil {
		mqtthelper.HandleError(err, "Set system state")
		return
	}

	switch s {
	case "connect":
		mqtthelper.HandleError(connect(m, h), "Connect")
	case "disconnect":
		disconnect(m, h)
	}
}

func setPlaybackState(h *HEOS, b []byte) {
	s, err := mediacenter.ParseSetPlaybackState(b)
	if err != nil {
		mqtthelper.HandleError(err, "Set playback state")
		return
	}

	if !h.IsConnected() {
		log.Printf("Can't set playback state (%s) when not connected", s)
		return
	}

	switch s {
	case "play":
		mqtthelper.HandleError(h.SetPlayState(players[0].PID, "play"), "Set playback state (play)")
	case "pause":
		mqtthelper.HandleError(h.SetPlayState(players[0].PID, "pause"), "Set playback state (pause)")
	case "stop":
		mqtthelper.HandleError(h.SetPlayState(players[0].PID, "stop"), "Set playback state (stop)")
	case "previous":
		mqtthelper.HandleError(h.PlayPrevious(players[0].PID), "Set playback state (previous)")
	case "next":
		mqtthelper.HandleError(h.PlayNext(players[0].PID), "Set playback state (next)")
	}
}

func play(h *HEOS, topic string, b []byte) {
	k := mediacenter.Kind(topic[strings.LastIndex(topic, "/")+1:])

	p, err := mediacenter.ParsePlay(k, b)
	if err != nil {
		mqtthelper.HandleError(err, "Play")
		return
	}

	switch p.Kind {
	case "station":
		source := sources.Get("TuneIn")
		if source == nil {
			mqtthelper.HandleError(errors.New("Source TuneIn not found"), "Play (station)")
			return
		}

		criteriaList, err := h.GetSearchCriteria(source.SID)
		if err != nil {
			mqtthelper.HandleError(err, "Play (station)")
			return
		}
		criteria := criteriaList.Get("Station")
		if criteria == nil {
			mqtthelper.HandleError(errors.New("Criteria for station not found"), "Play (station)")
			return
		}

		station := string(p.What.(mediacenter.PlayItemStation))
		result, err := h.Search(source.SID, criteria.SCID, station, 1, 1)
		if err != nil {
			mqtthelper.HandleError(err, "Play (station)")
			return
		}
		if result.Total == 0 {
			mqtthelper.HandleError(fmt.Errorf("Station '%s' could not be found", station), "Play (station)")
			return
		}

		mqtthelper.HandleError(h.PlayStation(players[0].PID, source.SID, result.Result[0].CID, result.Result[0].MID, result.Result[0].Name), "Play (station)")
	default:
		log.Printf("Playing of '%s' is not supported", p.Kind)
	}
}

func connect(m mqtt.Client, h *HEOS) error {
	if err := h.Connect(); err != nil {
		disconnect(m, h)
		return err
	}

	if err := h.RegisterForChangeEvents(false); err != nil {
		return err
	}

	if err := updatePlayers(h); err != nil {
		disconnect(m, h)
		return err
	}
	if err := updateSources(h); err != nil {
		disconnect(m, h)
		return err
	}

	if err := h.RegisterForChangeEvents(true); err != nil {
		return err
	}

	mqtthelper.PublishMessage(m, cfg.Topics.SystemState, 0, true, "connected")

	if len(players) > 0 {
		info.Model = players[0].Model
		info.Version = players[0].Version
		info.SerialNumber = players[0].SerialNumber
		mqtthelper.PublishCustomMessage(m, cfg.Topics.Info, 0, true, info)
	}

	mqtthelper.HandleError(updatePlayback(m, h), "Playback")

	return nil
}

func disconnect(m mqtt.Client, h *HEOS) {
	if h.IsConnected() {
		h.RegisterForChangeEvents(false)
		h.Disconnect()
	}
	mqtthelper.PublishMessage(m, cfg.Topics.SystemState, 0, true, "disconnected")
}

func updatePlayers(h *HEOS) error {
	p, err := h.GetPlayers()
	if err != nil {
		return err
	}
	if len(p) == 0 {
		return errors.New("No players found, cannot continue")
	}

	players = p
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

func updatePlayback(c mqtt.Client, h *HEOS) error {
	if !h.IsConnected() {
		publishPlayback(c)
		return nil
	}

	state, err := h.GetPlayState(players[0].PID)
	if err != nil {
		return err
	}
	playback.State = state

	switch playback.State {
	case "stop":
		publishPlayback(c)
		return nil
	case "play":
		playback.Speed = 1
	}

	p, err := h.GetPlayback(players[0].PID)
	if err != nil {
		return err
	}

	playback.Item = &mediacenter.PlaybackItem{
		Title: p.Song,
		Type:  p.Type,
	}

	switch p.Type {
	case "station":
		playback.Type = "station"
		playback.Item.Station = &mediacenter.PlaybackItemStation{
			Name: p.Station,
		}
	case "song":
		playback.Type = "song"
		playback.Item.Song = &mediacenter.PlaybackItemSong{
			Artist: p.Artist,
			Album:  p.Album,
		}
	}

	publishPlayback(c)
	return nil
}

func updatePlaybackState(c mqtt.Client, state string) {
	playback.State = state

	publishPlayback(c)
}

func updatePlaybackProgress(c mqtt.Client, h *HEOS, elapsed string, duration string) {
	e, err := strconv.Atoi(elapsed)
	if err != nil {
		mqtthelper.HandleError(err, "Playback progress (elapsed)")
		return
	}
	d, err := strconv.Atoi(duration)
	if err != nil {
		mqtthelper.HandleError(err, "Playback progress (duration)")
		return
	}

	playback.Duration = mediacenter.PlaybackDuration(time.Duration(d) * time.Millisecond)
	if d > 0 {
		playback.Elapsed = mediacenter.PlaybackDuration(time.Duration(e) * time.Millisecond)
		playback.StartTime = mediacenter.NewPlaybackTime(time.Now().Add(-playback.Elapsed.Duration()))
		playback.EndTime = mediacenter.NewPlaybackTime(playback.StartTime.Time().Add(playback.Duration.Duration()))
	} else {
		playback.Elapsed = mediacenter.PlaybackDuration(0)
		playback.StartTime = nil
		playback.EndTime = nil
	}

	publishPlayback(c)
}

func publishPlayback(c mqtt.Client) {
	mqtthelper.PublishCustomMessage(c, cfg.Topics.State, 0, true, playback)
}
