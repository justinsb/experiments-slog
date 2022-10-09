package attrs

import "golang.org/x/exp/slog"

func HTTPMethod(method string) slog.Attr {
	return slog.String("http.method", method)
}

func HTTPURL(url string) slog.Attr {
	return slog.String("http.url", url)
}
