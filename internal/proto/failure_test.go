package proto

import (
	"bytes"
	"testing"
)

func TestFailureEnvelopeNativeShape(t *testing.T) {
	want := []byte{1, 253, 255, 255, 255, 42, 0, 0, 0,
		3, 0, 0, 0, 'E', 'r', 'r', 4, 0, 0, 0, 'f', 'a', 'i', 'l'}
	if got := BuildFailureEnvelope(42, "Err", "fail"); !bytes.Equal(got, want) {
		t.Fatalf("原生 Failure 编码不符: %x", got)
	}
	env, ok := ParseEnvelope(want)
	if !ok || env.Marker != -3 || env.CallID != 42 {
		t.Fatalf("错误信封解析不符: %+v", env)
	}
}
