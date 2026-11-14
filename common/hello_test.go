package common

import (
	"bytes"
	"errors"
	"strings"
	"testing"
)

func TestConsumerHello_RoundTrip(t *testing.T) {
	want := ConsumerHello{Tag: "unbounded-out-trackA-r42", ClientVersion: "9.1.0", Platform: "android"}
	var buf bytes.Buffer
	if err := WriteConsumerHello(&buf, want); err != nil {
		t.Fatalf("WriteConsumerHello: %v", err)
	}
	got, err := ReadConsumerHello(&buf)
	if err != nil {
		t.Fatalf("ReadConsumerHello: %v", err)
	}
	if got != want {
		t.Fatalf("round trip = %+v, want %+v", got, want)
	}
}

func TestReadConsumerHello_RejectsOtherStreams(t *testing.T) {
	// A SOCKS5 greeting is what an ordinary stream starts with.
	_, err := ReadConsumerHello(bytes.NewReader([]byte{0x05, 0x01, 0x00}))
	if !errors.Is(err, ErrNotConsumerHello) {
		t.Fatalf("err = %v, want ErrNotConsumerHello", err)
	}
}

func TestReadConsumerHello_RejectsOversize(t *testing.T) {
	r := strings.NewReader(consumerHelloMagic + `{"tag":"` + strings.Repeat("x", MaxConsumerHelloSize) + `"}`)
	if _, err := ReadConsumerHello(r); err == nil {
		t.Fatal("oversize hello was accepted")
	}
}

func TestReadConsumerHello_IgnoresUnknownFields(t *testing.T) {
	r := strings.NewReader(consumerHelloMagic + `{"tag":"t","added_later":"x"}`)
	got, err := ReadConsumerHello(r)
	if err != nil {
		t.Fatalf("ReadConsumerHello: %v", err)
	}
	if got != (ConsumerHello{Tag: "t"}) {
		t.Fatalf("got %+v", got)
	}
}

func TestReadConsumerHello_SanitizesFields(t *testing.T) {
	r := strings.NewReader(consumerHelloMagic + `{"tag":"a\nb\u0000c","platform":"` + strings.Repeat("p", 200) + `"}`)
	got, err := ReadConsumerHello(r)
	if err != nil {
		t.Fatalf("ReadConsumerHello: %v", err)
	}
	if got.Tag != "abc" {
		t.Errorf("Tag = %q, want unprintable characters stripped", got.Tag)
	}
	if len(got.Platform) != maxConsumerHelloField {
		t.Errorf("len(Platform) = %d, want %d", len(got.Platform), maxConsumerHelloField)
	}
}
