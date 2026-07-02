package display

// maxChunk is the largest SPI data write; spidev's default bufsiz is 4096 B
// while a full frame is 8192 B, so RAM writes must be split.
const maxChunk = 4096

// chunk splits p into slices of at most max bytes (views into p, not copies).
//
//nolint:revive // "max" param name matches the task spec verbatim; shadows the max builtin only within this function.
func chunk(p []byte, max int) [][]byte {
	if len(p) == 0 {
		return nil
	}
	var out [][]byte
	for len(p) > 0 {
		n := max
		if n > len(p) {
			n = len(p)
		}
		out = append(out, p[:n])
		p = p[n:]
	}
	return out
}
