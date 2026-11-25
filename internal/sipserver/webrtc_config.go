package sipserver

import (
	"github.com/pion/webrtc/v4"
)

func (s *Server) iceServers() []webrtc.ICEServer {
	out := make([]webrtc.ICEServer, 0, len(s.webrtcCfg.ICEServers))
	for _, srv := range s.webrtcCfg.ICEServers {
		if len(srv.URLs) == 0 {
			continue
		}
		out = append(out, webrtc.ICEServer{
			URLs:       append([]string(nil), srv.URLs...),
			Username:   srv.Username,
			Credential: srv.Credential,
		})
	}
	return out
}

func (s *Server) newPeerConnection() (*webrtc.PeerConnection, error) {
	setting := webrtc.SettingEngine{}
	if s.webrtcCfg.PublicIP != "" {
		setting.SetNAT1To1IPs([]string{s.webrtcCfg.PublicIP}, webrtc.ICECandidateTypeHost)
	}
	api := webrtc.NewAPI(webrtc.WithSettingEngine(setting))
	return api.NewPeerConnection(webrtc.Configuration{ICEServers: s.iceServers()})
}
