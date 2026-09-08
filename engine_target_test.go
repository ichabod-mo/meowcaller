package meowcaller

import (
	"testing"

	"go.mau.fi/whatsmeow/types"
)

func TestParseCallTargetNormalizesLegacyPhoneJID(t *testing.T) {
	legacy, err := parseCallTarget("5491162502232@c.us")
	if err != nil {
		t.Fatal(err)
	}
	modern, err := parseCallTarget("5491162502232@s.whatsapp.net")
	if err != nil {
		t.Fatal(err)
	}
	if legacy != modern {
		t.Fatalf("legacy phone JID %v was not normalized to %v", legacy, modern)
	}
	if legacy.Server != types.DefaultUserServer {
		t.Fatalf("normalized phone JID server = %q, want %q", legacy.Server, types.DefaultUserServer)
	}
}
