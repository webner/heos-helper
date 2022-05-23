package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

type Response struct {
	HEOS struct {
		Command string `json:"command"`
		Result  string `json:"result"`
		Message string `json:"message"`
		Params  url.Values
	} `json:"heos"`
	Payload json.RawMessage `json:"payload"`
}

type Player struct {
	Name         string `json:"name"`
	PID          int    `json:"pid"`
	GID          int    `json:"gid"`
	Model        string `json:"model"`
	Version      string `json:"version"`
	Network      string `json:"network"`
	Lineout      int    `json:"lineout"`
	Control      string `json:"control"`
	SerialNumber string `json:"serial"`
}

type PlayerVolume struct {
	PID   int    `json:"pid"`
	Level int    `json:"level"`
	Mute  string `json:"mute"`
}

type Playback struct {
	Type     string `json:"type"`
	Song     string `json:"song"`
	Album    string `json:"album"`
	AlbumID  string `json:"album_id"`
	Artist   string `json:"artist"`
	Station  string `json:"station"`
	ImageUrl string `json:"image_url"`
	MID      string `json:"mid"`
	QID      int    `json:"qid"`
	SID      int    `json:"sid"`
}

type Source struct {
	Type            string `json:"type"`
	Name            string `json:"name"`
	SID             int    `json:"sid"`
	ImageUrl        string `json:"image_url"`
	Available       string `json:"available"`
	ServiceUsername string `json:"service_username"`
}

type SourceList []Source

func (s SourceList) Get(name string) *Source {
	for _, v := range s {
		if v.Name == name {
			return &v
		}
	}

	return nil
}

type Service struct {
	Type     string `json:"type"`
	Name     string `json:"name"`
	SID      int    `json:"sid"`
	ImageUrl string `json:"image_url"`
}

type Media struct {
	Container string `json:"container"`
	Playable  string `json:"playable"`
	Type      string `json:"type"`
	Name      string `json:"name"`
	Artist    string `json:"artist"`
	Album     string `json:"album"`
	ImageUrl  string `json:"image_url"`
	CID       string `json:"cid"`
	MID       string `json:"mid"`
}

type SearchResult struct {
	Total  int
	Result []Media
}

type SearchCriteria struct {
	Playable string `json:"playable"`
	Wildcard string `json:"wildcard"`
	Name     string `json:"name"`
	SCID     int    `json:"scid"`
	CID      string `json:"cid"`
}

type SearchCriteriaList []SearchCriteria

func (s SearchCriteriaList) Get(name string) *SearchCriteria {
	for _, v := range s {
		if v.Name == name {
			return &v
		}
	}

	return nil
}

type Event struct {
	Type   string
	Params url.Values
}

type HEOS struct {
	uri           string
	conn          net.Conn
	mutex         sync.Mutex
	responseWaits map[string]*responseWait
	EventCh       chan Event
}

func NewHEOS(uri string) *HEOS {
	return &HEOS{
		uri:           uri,
		EventCh:       make(chan Event, 10),
		responseWaits: make(map[string]*responseWait),
	}
}

func (h *HEOS) IsConnected() bool {
	return h.conn != nil
}

func (h *HEOS) Connect() error {
	if !h.IsConnected() {

		hosts := strings.Split(h.uri, ",")
		h.conn = nil

		var lastErr error

		for _, host := range hosts {

			log.Printf("Connecting to %v", host)
			c, err := net.DialTimeout("tcp", host, time.Duration(time.Second*3))
			if err != nil {
				log.Printf("Connecting failed to %v: %v", host, err)
				time.Sleep(time.Second)
				lastErr = err
				continue
			}
			log.Printf("Connecting established to %v", host)
			h.conn = c
			break
		}

		if h.conn == nil {
			return lastErr
		}

		disconnectChan := make(chan bool)

		go func() {
			log.Printf("Receive function started")
			defer close(disconnectChan)

			result := make([]byte, 0)
			buf := make([]byte, 30)

			for h.IsConnected() {
				n, err := h.conn.Read(buf)
				if err != nil {
					log.Printf("Receive error %v", err)
					h.Disconnect()
					break
				}
				if n != 0 {
					result = append(result, buf[:n]...)

					for h.IsConnected() {
						ev := bytes.SplitN(result, []byte{0xd, 0xa}, 2)
						if len(ev) != 2 {
							break
						}
						if len(ev[0]) != 0 {
							r := Response{}
							if err := json.Unmarshal(ev[0], &r); err != nil {
								log.Printf("Could not parse received data '%s': %s", string(ev[0]), err)
								continue
							}
							if q, err := url.ParseQuery(r.HEOS.Message); err == nil {
								r.HEOS.Params = q
							}

							cmdParts := strings.Split(r.HEOS.Command, "/")
							if len(cmdParts) > 1 && cmdParts[0] == "event" {
								h.EventCh <- Event{
									Type:   cmdParts[1],
									Params: r.HEOS.Params,
								}
							} else {
								if w, found := h.responseWaits[r.HEOS.Command]; found {
									w.response = r
									if r.HEOS.Result == "fail" {
										w.done <- fmt.Errorf("%s (code: %s)", w.response.HEOS.Params.Get("text"), w.response.HEOS.Params.Get("eid"))
									} else {
										if strings.HasPrefix(w.response.HEOS.Message, "command under process") {
											log.Printf("Command %s under process, waiting for response", w.response.HEOS.Command)
										} else {
											w.done <- nil
										}
									}
								} else {
									log.Printf("Response for command '%s' received but no request found", r.HEOS.Command)
								}
							}
						}
						result = ev[1]
					}
				}
			}
			log.Printf("Receive function stopped")
		}()

		go func() {
			log.Printf("Heart beat function started")
			t := time.NewTicker(3 * time.Second)
			defer t.Stop()
			for h.IsConnected() {
				select {
				case <-disconnectChan:
					log.Printf("disconnectChan triggered")
					break
				case <-t.C:
					if err := h.HeartBeat(); err != nil {
						log.Printf("Heart beat failed: %s", err)
						h.Disconnect()
						break
					}
				}
			}
			log.Printf("Heart beat function stopped")
		}()
	}

	return nil
}

func (h *HEOS) Disconnect() {
	if h.IsConnected() {
		h.conn.Close()
		h.conn = nil
		h.EventCh <- Event{
			Type: "disconnected",
		}
	}
}

func (h *HEOS) RegisterForChangeEvents(enable bool) error {
	params := map[string]string{
		"enable": "off",
	}
	if enable {
		params["enable"] = "on"
	}

	_, err := h.sendRequest("system/register_for_change_events", params)
	return err
}

func (h *HEOS) HeartBeat() error {
	_, err := h.sendRequest("system/heart_beat", nil)
	return err
}

func (h *HEOS) GetPlayers() ([]Player, error) {
	players := []Player{}
	r, err := h.sendRequest("player/get_players", nil)
	if err != nil {
		return players, err
	}
	err = json.Unmarshal(r.Payload, &players)
	return players, err
}

func (h *HEOS) GetPlayState(pid int) (string, error) {
	params := map[string]string{
		"pid": strconv.Itoa(pid),
	}
	r, err := h.sendRequest("player/get_play_state", params)
	if err != nil {
		return "", err
	}

	return r.HEOS.Params.Get("state"), nil
}

func (h *HEOS) SetPlayState(pid int, state string) error {
	log.Printf("Set PlayState: %v", state)
	params := map[string]string{
		"pid":   strconv.Itoa(pid),
		"state": state,
	}

	_, err := h.sendRequest("player/set_play_state", params)
	return err
}

func (h *HEOS) GetVolume(pid int) (int, error) {
	params := map[string]string{
		"pid": strconv.Itoa(pid),
	}
	r, err := h.sendRequest("player/get_volume", params)
	if err != nil {
		return 0, err
	}

	return strconv.Atoi(r.HEOS.Params.Get("level"))
}

func (h *HEOS) SetVolume(pid int, level int) error {
	params := map[string]string{
		"pid":   strconv.Itoa(pid),
		"level": strconv.Itoa(level),
	}

	_, err := h.sendRequest("player/set_volume", params)
	return err
}

func (h *HEOS) GetMute(pid int) (string, error) {
	params := map[string]string{
		"pid": strconv.Itoa(pid),
	}
	r, err := h.sendRequest("player/get_mute", params)
	if err != nil {
		return "", err
	}

	return r.HEOS.Params.Get("state"), nil
}

func (h *HEOS) SetMute(pid int, state string) error {
	params := map[string]string{
		"pid":   strconv.Itoa(pid),
		"state": state,
	}

	_, err := h.sendRequest("player/set_mute", params)
	return err
}

func (h *HEOS) GetPlayback(pid int) (Playback, error) {
	playback := Playback{}

	params := map[string]string{
		"pid": strconv.Itoa(pid),
	}
	r, err := h.sendRequest("player/get_now_playing_media", params)
	if err != nil {
		return playback, err
	}

	err = json.Unmarshal(r.Payload, &playback)
	return playback, err
}

func (h *HEOS) PlayNext(pid int) error {
	params := map[string]string{
		"pid": strconv.Itoa(pid),
	}

	_, err := h.sendRequest("player/play_next", params)
	return err
}

func (h *HEOS) PlayPrevious(pid int) error {
	params := map[string]string{
		"pid": strconv.Itoa(pid),
	}

	_, err := h.sendRequest("player/play_previous", params)
	return err
}

func (h *HEOS) GetSources() (SourceList, error) {
	sources := SourceList{}
	r, err := h.sendRequest("browse/get_music_sources", nil)
	if err != nil {
		return sources, err
	}

	err = json.Unmarshal(r.Payload, &sources)
	return sources, err
}

func (h *HEOS) GetSearchCriteria(sid int) (SearchCriteriaList, error) {
	criteria := SearchCriteriaList{}

	params := map[string]string{
		"sid": strconv.Itoa(sid),
	}
	r, err := h.sendRequest("browse/get_search_criteria", params)
	if err != nil {
		return criteria, err
	}

	err = json.Unmarshal(r.Payload, &criteria)
	return criteria, err
}

func (h *HEOS) GetSourceServices(sid int) ([]Service, error) {
	services := []Service{}

	params := map[string]string{
		"sid": strconv.Itoa(sid),
	}
	r, err := h.sendRequest("browse/browse", params)
	if err != nil {
		return services, err
	}

	err = json.Unmarshal(r.Payload, &services)
	return services, err
}

func (h *HEOS) BrowseSource(sid int) ([]Media, error) {
	medias := []Media{}

	params := map[string]string{
		"sid": strconv.Itoa(sid),
	}
	r, err := h.sendRequest("browse/browse", params)
	if err != nil {
		return medias, err
	}

	err = json.Unmarshal(r.Payload, &medias)
	return medias, err
}

func (h *HEOS) BrowseSourceContainer(sid int, cid string, start int, end int) ([]Media, error) {
	medias := []Media{}

	params := map[string]string{
		"sid":   strconv.Itoa(sid),
		"cid":   cid,
		"range": fmt.Sprintf("%s,%s", strconv.Itoa(start), strconv.Itoa(end)),
	}
	r, err := h.sendRequest("browse/browse", params)
	if err != nil {
		return medias, err
	}

	err = json.Unmarshal(r.Payload, &medias)
	return medias, err
}

func (h *HEOS) Search(sid int, scid int, search string, start int, end int) (SearchResult, error) {
	result := SearchResult{}

	params := map[string]string{
		"sid":    strconv.Itoa(sid),
		"scid":   strconv.Itoa(scid),
		"search": search,
		"range":  fmt.Sprintf("%s,%s", strconv.Itoa(start), strconv.Itoa(end)),
	}
	r, err := h.sendRequest("browse/search", params)
	if err != nil {
		return result, err
	}

	if c, err := strconv.Atoi(r.HEOS.Params.Get("count")); err != nil {
		return result, err
	} else {
		result.Total = c
	}

	err = json.Unmarshal(r.Payload, &result.Result)
	return result, err
}

func (h *HEOS) PlayStation(pid int, sid int, cid string, mid string, name string) error {
	params := map[string]string{
		"pid": strconv.Itoa(pid),
		"sid": strconv.Itoa(sid),
	}
	if cid != "" {
		params["cid"] = cid
	}
	if mid != "" {
		params["mid"] = mid
	}
	if name != "" {
		params["name"] = name
	}

	_, err := h.sendRequest("player/play_stream", params)
	return err
}

func (h *HEOS) sendRequest(cmd string, params map[string]string) (Response, error) {
	r := Response{}

	if !h.IsConnected() {
		return r, errors.New("Not connected")
	}

	uri := url.URL{}
	uri.Scheme = "heos"
	uri.Path = cmd
	q := url.Values{}
	for k, v := range params {
		q.Set(k, v)
	}
	uri.RawQuery = q.Encode()

	w, err := h.registerResponseWait(cmd)
	if err != nil {
		return r, err
	}
	defer h.unregisterResponseWait(cmd)

	payload := []byte(uri.String() + "\r\n")
	_, err = h.conn.Write(payload)
	if err != nil {
		return r, err
	}

	t := time.After(5 * time.Second)
	select {
	case err := <-w.done:
		return w.response, err
	case <-t:
		return r, fmt.Errorf("Waiting for response timed out for %s", uri.String())
	}
}

func (h *HEOS) registerResponseWait(command string) (*responseWait, error) {
	w := newResponseWait(command)

	if _, found := h.responseWaits[command]; found {
		return w, fmt.Errorf("Parallel requests are not supported (%s)", command)
	}

	h.mutex.Lock()
	h.responseWaits[command] = w
	h.mutex.Unlock()

	return w, nil
}

func (h *HEOS) unregisterResponseWait(command string) {
	h.mutex.Lock()
	defer h.mutex.Unlock()
	if _, found := h.responseWaits[command]; !found {
		return
	}

	waits := make(map[string]*responseWait, len(h.responseWaits))
	for k, v := range h.responseWaits {
		if k != command {
			waits[k] = v
		}
	}
	h.responseWaits = waits
}

type responseWait struct {
	command  string
	response Response
	done     chan error
}

func newResponseWait(command string) *responseWait {
	return &responseWait{
		command:  command,
		response: Response{},
		done:     make(chan error),
	}
}
