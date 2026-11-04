package proto

// BuildFailureEnvelope 保留原 call_id，错误字符串使用原生 Int32 长度前缀。
func BuildFailureEnvelope(callID uint32, errorType, message string) []byte {
	out := append([]byte{0x01}, PackI32(-3)...)
	out = append(out, PackU32(callID)...)
	for _, text := range []string{errorType, message} {
		out = append(out, PackU32(uint32(len([]byte(text))))...)
		out = append(out, []byte(text)...)
	}
	return out
}
