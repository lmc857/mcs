package api

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"mcs/internal/config"
	"mcs/internal/sipserver"
	"mcs/internal/store"
)

func TestWebSocketAlarmEvent(t *testing.T) {
	st := store.New([]config.DeviceConfig{{Ext: "1001", Password: "pass-1001"}})
	cdr := store.NewCDRStore(10)
	dir := t.TempDir()
	alarms := store.NewAlarmStore(10)
	sip, err := sipserver.New(st, sipserver.NewAuthenticator("mcs", "test"), cdr, alarms, sipserver.Options{
		Domain: "mcs.local", Advertise: "127.0.0.1:15060", DataDir: dir,
		Alarm: config.AlarmConfig{Number: "8000"},
	}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	defer sip.Close()

	ts := httptest.NewServer(New(st, cdr, alarms, sip, dir, "test").Handler())
	defer ts.Close()

	wsURL := strings.Replace(ts.URL, "http://", "ws://", 1) + "/api/v1/ws"
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	conn, _, err := websocket.Dial(ctx, wsURL, nil)
	if err != nil {
		t.Fatalf("dial ws: %v", err)
	}
	defer conn.Close(websocket.StatusNormalClosure, "")

	time.Sleep(50 * time.Millisecond)
	sip.EmitTestAlarm("1002", "1001")

	_, data, err := conn.Read(ctx)
	if err != nil {
		t.Fatalf("read ws: %v", err)
	}
	var ev struct {
		Type string `json:"type"`
		Data struct {
			From string `json:"from"`
			To   string `json:"to"`
		} `json:"data"`
	}
	if err := json.Unmarshal(data, &ev); err != nil {
		t.Fatal(err)
	}
	if ev.Type != "alarm.created" {
		t.Fatalf("event type = %q", ev.Type)
	}
	if ev.Data.From != "1002" || ev.Data.To != "1001" {
		t.Fatalf("unexpected data: %+v", ev.Data)
	}
}

func TestAlarmTestEndpoint(t *testing.T) {
	st := store.New(nil)
	cdr := store.NewCDRStore(10)
	dir := t.TempDir()
	alarms := store.NewAlarmStore(10)
	sip, err := sipserver.New(st, sipserver.NewAuthenticator("mcs", "test"), cdr, alarms, sipserver.Options{
		Domain: "mcs.local", Advertise: "127.0.0.1:15060", DataDir: dir,
		Alarm: config.AlarmConfig{Number: "8000"},
	}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	defer sip.Close()
	ts := httptest.NewServer(New(st, cdr, alarms, sip, dir, "test").Handler())
	defer ts.Close()

	res, err := http.Post(ts.URL+"/api/v1/alarm/test", "application/json", strings.NewReader(`{"from":"1002","to":"1001"}`))
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != 202 {
		t.Fatalf("status = %d", res.StatusCode)
	}

	// The test alarm is stored and can be closed via the API.
	listRes, err := http.Get(ts.URL + "/api/v1/alarms")
	if err != nil {
		t.Fatal(err)
	}
	defer listRes.Body.Close()
	var list struct {
		Items []struct {
			ID string `json:"id"`
		} `json:"items"`
	}
	if err := json.NewDecoder(listRes.Body).Decode(&list); err != nil {
		t.Fatal(err)
	}
	if len(list.Items) != 1 {
		t.Fatalf("alarms = %d", len(list.Items))
	}
	id := list.Items[0].ID
	closeRes, err := http.Post(ts.URL+"/api/v1/alarms/"+id+"/close", "application/json", strings.NewReader(`{"disposition":"test","note":"ok"}`))
	if err != nil {
		t.Fatal(err)
	}
	defer closeRes.Body.Close()
	if closeRes.StatusCode != 200 {
		t.Fatalf("close status = %d", closeRes.StatusCode)
	}
	var closed struct {
		State       string `json:"state"`
		Disposition string `json:"disposition"`
	}
	if err := json.NewDecoder(closeRes.Body).Decode(&closed); err != nil {
		t.Fatal(err)
	}
	if closed.State != "closed" || closed.Disposition != "test" {
		t.Fatalf("closed = %+v", closed)
	}
}

func TestDeviceSnapshotProxy(t *testing.T) {
	jpeg := []byte{0xFF, 0xD8, 0xFF, 0xE0, 0x00, 0x10, 'J', 'F', 'I', 'F', 0xFF, 0xD9}
	imgSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/jpeg")
		_, _ = w.Write(jpeg)
	}))
	defer imgSrv.Close()

	st := store.New([]config.DeviceConfig{{Ext: "1001", Password: "pass-1001", Snapshot: imgSrv.URL}})
	cdr := store.NewCDRStore(10)
	alarms := store.NewAlarmStore(10)
	dir := t.TempDir()
	sip, err := sipserver.New(st, sipserver.NewAuthenticator("mcs", "test"), cdr, alarms, sipserver.Options{
		Domain: "mcs.local", Advertise: "127.0.0.1:15060", DataDir: dir,
		Alarm: config.AlarmConfig{Number: "8000"},
	}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	defer sip.Close()
	ts := httptest.NewServer(New(st, cdr, alarms, sip, dir, "test").Handler())
	defer ts.Close()

	res, err := http.Get(ts.URL + "/api/v1/devices/1001/snapshot")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		t.Fatalf("status = %d", res.StatusCode)
	}
	if ct := res.Header.Get("Content-Type"); ct != "image/jpeg" {
		t.Fatalf("content-type = %q", ct)
	}
	body, _ := io.ReadAll(res.Body)
	if string(body) != string(jpeg) {
		t.Fatalf("body = %v", body)
	}
}

func TestConsoleAuthAndDeviceCRUD(t *testing.T) {
	st := store.New(nil)
	cdr := store.NewCDRStore(10)
	alarms := store.NewAlarmStore(10)
	dir := t.TempDir()
	sip, err := sipserver.New(st, sipserver.NewAuthenticator("mcs", "test"), cdr, alarms, sipserver.Options{
		Domain: "mcs.local", Advertise: "127.0.0.1:15060", DataDir: dir,
		Alarm: config.AlarmConfig{Number: "8000"},
	}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	defer sip.Close()
	ts := httptest.NewServer(New(st, cdr, alarms, sip, dir, "test", WithConsoleAuth("admin", "s3cret")).Handler())
	defer ts.Close()
	client := ts.Client()

	res, err := client.Get(ts.URL + "/api/v1/devices")
	if err != nil {
		t.Fatal(err)
	}
	if res.StatusCode != 401 {
		t.Fatalf("unauthenticated status = %d", res.StatusCode)
	}
	res.Body.Close()

	res, err = client.Post(ts.URL+"/api/v1/login", "application/json", strings.NewReader(`{"username":"admin","password":"wrong"}`))
	if err != nil {
		t.Fatal(err)
	}
	if res.StatusCode != 401 {
		t.Fatalf("wrong password status = %d", res.StatusCode)
	}
	res.Body.Close()

	res, err = client.Post(ts.URL+"/api/v1/login", "application/json", strings.NewReader(`{"username":"admin","password":"s3cret"}`))
	if err != nil {
		t.Fatal(err)
	}
	if res.StatusCode != 200 {
		t.Fatalf("login status = %d", res.StatusCode)
	}
	cookies := res.Cookies()
	res.Body.Close()
	if len(cookies) == 0 {
		t.Fatal("login set no cookie")
	}
	cookie := cookies[0]

	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/api/v1/devices", strings.NewReader(`{"ext":"1009","name":"test door","password":"pass-1009","zone":"A"}`))
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(cookie)
	res, err = client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if res.StatusCode != 201 {
		body, _ := io.ReadAll(res.Body)
		t.Fatalf("create device status = %d body=%s", res.StatusCode, body)
	}
	res.Body.Close()
	if !st.Register("1009", "<sip:1009@127.0.0.1:15060>", "127.0.0.1:15060", "udp", "", "", "", time.Minute) {
		t.Fatal("register test device")
	}
	req, _ = http.NewRequest(http.MethodPatch, ts.URL+"/api/v1/devices/1009", strings.NewReader(`{"password":"new-pass-1009","name":"renamed"}`))
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(cookie)
	res, err = client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if res.StatusCode != 200 {
		body, _ := io.ReadAll(res.Body)
		t.Fatalf("patch device status = %d body=%s", res.StatusCode, body)
	}
	res.Body.Close()
	if !st.Authenticate("1009", "new-pass-1009") || st.Authenticate("1009", "pass-1009") {
		t.Fatal("device password was not updated")
	}
	if dev, _ := st.Get("1009"); dev.Online {
		t.Fatal("password change did not drop old registration")
	}

	req, _ = http.NewRequest(http.MethodGet, ts.URL+"/api/v1/devices", nil)
	req.AddCookie(cookie)
	res, err = client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if res.StatusCode != 200 {
		t.Fatalf("devices status = %d", res.StatusCode)
	}
	res.Body.Close()

	req, _ = http.NewRequest(http.MethodPost, ts.URL+"/api/v1/logout", nil)
	req.AddCookie(cookie)
	res, err = client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()

	req, _ = http.NewRequest(http.MethodGet, ts.URL+"/api/v1/devices", nil)
	req.AddCookie(cookie)
	res, err = client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if res.StatusCode != 401 {
		t.Fatalf("post-logout status = %d", res.StatusCode)
	}
	res.Body.Close()
}

func testTTSWAV() []byte {
	const n = 800
	data := make([]byte, 44+n*2)
	copy(data[0:4], "RIFF")
	data[4] = byte((36 + n*2) & 0xff)
	data[5] = byte(((36 + n*2) >> 8) & 0xff)
	copy(data[8:12], "WAVE")
	copy(data[12:16], "fmt ")
	data[16] = 16
	data[20] = 1
	data[22] = 1
	data[24] = 0x40
	data[25] = 0x1f
	data[28] = 0x80
	data[29] = 0x3e
	data[32] = 2
	data[34] = 16
	copy(data[36:40], "data")
	data[40] = byte((n * 2) & 0xff)
	data[41] = byte(((n * 2) >> 8) & 0xff)
	return data
}

func TestTTSConfigAPIAndPersistence(t *testing.T) {
	ttsSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "audio/wav")
		_, _ = w.Write(testTTSWAV())
	}))
	defer ttsSrv.Close()

	st := store.New(nil)
	cdr := store.NewCDRStore(10)
	alarms := store.NewAlarmStore(10)
	dir := t.TempDir()
	db, err := store.OpenDB(dir + "/mcs.db")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	sip, err := sipserver.New(st, sipserver.NewAuthenticator("mcs", "test"), cdr, alarms, sipserver.Options{
		Domain: "mcs.local", Advertise: "127.0.0.1:15060", DataDir: dir,
		Alarm: config.AlarmConfig{Number: "8000"},
		TTS:   config.TTSConfig{Provider: "openai", BaseURL: ttsSrv.URL, VoiceZH: "zf_001", TimeoutS: 5},
	}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	defer sip.Close()
	ts := httptest.NewServer(New(st, cdr, alarms, sip, dir, "test", WithDB(db)).Handler())
	defer ts.Close()

	res, err := http.Get(ts.URL + "/api/v1/tts/config")
	if err != nil {
		t.Fatal(err)
	}
	var view map[string]any
	_ = json.NewDecoder(res.Body).Decode(&view)
	res.Body.Close()
	if view["provider"] != "openai" || view["has_api_key"] != false {
		t.Fatalf("view = %+v", view)
	}

	res, err = http.Post(ts.URL+"/api/v1/tts/test", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.StatusCode != 200 {
		body, _ := io.ReadAll(res.Body)
		t.Fatalf("tts test = %d %s", res.StatusCode, body)
	}
	res.Body.Close()

	req, _ := http.NewRequest(http.MethodPut, ts.URL+"/api/v1/tts/config", strings.NewReader(`{"provider":"openai","base_url":"`+ttsSrv.URL+`","model":"tts-1","voice_zh":"alloy","voice_en":"alloy","timeout_s":5,"api_key":"secret"}`))
	req.Header.Set("Content-Type", "application/json")
	res, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != 200 {
		t.Fatalf("update = %d", res.StatusCode)
	}
	raw, ok := db.GetSetting("tts")
	if !ok || !strings.Contains(raw, "secret") {
		t.Fatalf("tts setting not persisted: %q", raw)
	}
}
