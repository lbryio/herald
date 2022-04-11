package server_test

import (
	"log"
	"os/exec"
	"strings"
	"testing"

	server "github.com/lbryio/hub/server"
)

// TestUDPPing tests UDPPing correctness against prod server.
func TestUDPPing(t *testing.T) {
	args := makeDefaultArgs()
	args.DisableStartUDP = true

	tests := []struct {
		name                string
		wantIP              string
		wantCountry         string
		wantProtocolVersion int
		wantHeightMin       int
		wantFlags           byte
	}{
		{
			name:                "Correctly parse information from production server.",
			wantIP:              "SETME",
			wantCountry:         "US",
			wantProtocolVersion: 1,
			wantHeightMin:       1060000,
			wantFlags:           1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {

			toAddr := "spv15.lbry.com"
			toPort := "50001"

			pong, err := server.UDPPing(toAddr, toPort)
			gotCountry := pong.DecodeCountry()
			if err != nil {
				log.Println(err)
			}

			res, err := exec.Command("dig", "@resolver4.opendns.com", "myip.opendns.com", "+short").Output()

			if err != nil {
				log.Println(err)
			}

			digIP := strings.TrimSpace(string(res))
			udpIP := pong.DecodeAddress().String()
			tt.wantIP = digIP

			log.Println("Height:", pong.DecodeHeight())
			log.Printf("Flags: %x\n", pong.DecodeFlags())
			log.Println("ProtocolVersion:", pong.DecodeProtocolVersion())
			log.Printf("Tip: %x\n", pong.DecodeTip())

			gotHeight := pong.DecodeHeight()
			gotProtocolVersion := pong.DecodeProtocolVersion()
			gotFlags := pong.DecodeFlags()
			gotIP := udpIP

			if gotIP != tt.wantIP {
				t.Errorf("ip: got: '%s', want: '%s'\n", gotIP, tt.wantIP)
			}
			if gotCountry != tt.wantCountry {
				t.Errorf("country: got: '%s', want: '%s'\n", gotCountry, tt.wantCountry)
			}
			if gotHeight < tt.wantHeightMin {
				t.Errorf("height: got: %d, want >=: %d\n", gotHeight, tt.wantHeightMin)
			}
			if gotProtocolVersion != tt.wantProtocolVersion {
				t.Errorf("protocolVersion: got: %d, want: %d\n", gotProtocolVersion, tt.wantProtocolVersion)
			}
			if gotFlags != tt.wantFlags {
				t.Errorf("flags: got: %d, want: %d\n", gotFlags, tt.wantFlags)
			}
		})
	}

}
