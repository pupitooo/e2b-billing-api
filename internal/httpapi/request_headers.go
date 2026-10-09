package httpapi

import (
	"mime"
	"net/http"
	"strings"
)

func usesJSONUTF8(r *http.Request) bool {
	mediaType, parameters, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	return err == nil && mediaType == "application/json" &&
		(parameters["charset"] == "" || strings.EqualFold(parameters["charset"], "utf-8"))
}

func usesIdentityEncoding(r *http.Request) bool {
	encoding := r.Header.Get("Content-Encoding")
	return encoding == "" || strings.EqualFold(encoding, "identity")
}
