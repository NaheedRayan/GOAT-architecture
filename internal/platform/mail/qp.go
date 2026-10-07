package mail

import (
	"bytes"
	"mime/quotedprintable"
)

func qpEncode(s string) string {
	var b bytes.Buffer
	w := quotedprintable.NewWriter(&b)
	_, _ = w.Write([]byte(s))
	_ = w.Close()
	return b.String()
}
