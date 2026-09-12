package main

import (
	"net/url"
	"reflect"
	"testing"
)

func TestRelayVariantsKeepInputAndPixelsAndAuthenticateRTSP(t *testing.T) {
	srt := relayTrialCommand(profile{Name: "relay-srt120"}, 1234, "test-only", "srt://127.0.0.1:1235")
	rtsp := relayTrialCommand(profile{Name: "relay-rtsp"}, 1234, "user:@/& secret", "unused")
	flush := relayTrialCommand(profile{Name: "relay-rtsp-flush"}, 1234, "user:@/& secret", "unused")
	if !reflect.DeepEqual(srt[:15], rtsp[:15]) || !reflect.DeepEqual(rtsp[:15], flush[:15]) {
		t.Fatal("variants changed relay input or video copying")
	}
	u, err := url.Parse(rtsp[len(rtsp)-1])
	if err != nil {
		t.Fatal(err)
	}
	pass, _ := u.User.Password()
	if u.Scheme != "rtsp" || u.Host != "127.0.0.1:1234" || u.Path != "/impaired" || u.User.Username() != "benchmark" || pass != "user:@/& secret" {
		t.Fatal("RTSP destination or escaped credentials changed")
	}
	if rtsp[len(rtsp)-3] != "-rtsp_transport" || rtsp[len(rtsp)-2] != "tcp" {
		t.Fatal("RTSP output did not require TCP")
	}
}
