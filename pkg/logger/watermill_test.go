// Copyright 2025 JAAB Tech SAS, Uruguay
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package logger

import (
	"bytes"
	"errors"
	"testing"

	"github.com/ThreeDotsLabs/watermill"
	"log/slog"
)

func TestWatermillAdapter(t *testing.T) {
	buf := &bytes.Buffer{}
	cfg := Config{
		Level:      "debug",
		EntityType: TypeRack,
		Name:       "test",
		Writer:     buf,
	}
	l := New(cfg)
	// WatermillAdapter uses slog.Default(), so we must replace the global default for this test
	original := slog.Default()
	slog.SetDefault(l)
	defer slog.SetDefault(original)

	adapter := NewWatermillAdapter(l)

	// Info
	adapter.Info("info message", watermill.LogFields{"key": "value"})
	if !bytes.Contains(buf.Bytes(), []byte("info message")) {
		t.Errorf("Info not logged. Got: %s", buf.String())
	}

	// Debug
	buf.Reset()
	adapter.Debug("debug message", watermill.LogFields{"d": 1})
	if !bytes.Contains(buf.Bytes(), []byte("debug message")) {
		t.Error("Debug not logged")
	}

	// Trace (maps to Debug)
	buf.Reset()
	adapter.Trace("trace message", nil)
	if !bytes.Contains(buf.Bytes(), []byte("trace message")) {
		t.Error("Trace not logged")
	}

	// Error
	buf.Reset()
	adapter.Error("error message", errors.New("test error"), watermill.LogFields{"e": "v"})
	if !bytes.Contains(buf.Bytes(), []byte("error message")) {
		t.Error("Error not logged")
	}
	if !bytes.Contains(buf.Bytes(), []byte("test error")) {
		t.Error("Error value not logged")
	}

	// With
	adapted := adapter.With(watermill.LogFields{"extra": true})
	if adapted == nil {
		t.Error("With returned nil")
	}

	// toAttrs with empty
	attrs := adapter.toAttrs(nil)
	if attrs != nil {
		t.Error("toAttrs should return nil for empty fields")
	}
}
