package bus

import (
	"testing"
	"unicode/utf8"

	"github.com/jaab-tech/fluxrig/pkg/fluxmsg"
	"github.com/stretchr/testify/assert"
)

func TestFluxMsgTransparentSanitization(t *testing.T) {
	msg := fluxmsg.New()

	// 1. Test Valid UTF-8
	msg.SetMetadata("test.valid", "hello world")
	assert.Equal(t, "hello world", msg.Metadata["test.valid"])

	// 2. Test Invalid UTF-8 (Raw Binary)
	binaryData := string([]byte{0xff, 0xfe, 0xfd})
	msg.SetMetadata("test.binary", binaryData)

	// Should be hex-encoded transparently
	assert.True(t, utf8.ValidString(msg.Metadata["test.binary"]))
	assert.Contains(t, msg.Metadata["test.binary"], "hex:fffefd")

	// 3. Test MetadataBytes helper
	msg.SetMetadataBytes("test.bytes", []byte{0xde, 0xad, 0xbe, 0xef})
	assert.Equal(t, "hex:deadbeef", msg.Metadata["test.bytes"])
}

func TestFluxMsgValidationGuard(t *testing.T) {
	msg := fluxmsg.New()

	// 1. Valid Message
	msg.Metadata["key"] = "value"
	assert.NoError(t, msg.Validate())

	// 2. Poisoned Metadata (Manual Map Injection)
	msg.Metadata["poison"] = string([]byte{0x80, 0x81})
	err := msg.Validate()
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "invalid UTF-8 in metadata value")

	// 3. Poisoned Data Key
	msg2 := fluxmsg.New()
	msg2.Data[string([]byte{0x80})] = "val"
	err2 := msg2.Validate()
	assert.Error(t, err2)
	assert.Contains(t, err2.Error(), "invalid UTF-8 in data key")
}
