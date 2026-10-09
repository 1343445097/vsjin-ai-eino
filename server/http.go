package server

import (
	"context"
	"crypto/subtle"
	"github.com/cloudwego/hertz/pkg/app"
	"net/url"
	"strings"
)

type SkillSummary struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}

func (s *Server[M]) authorizeRequest(ctx context.Context, c *app.RequestContext) {
	path := string(c.Path())
	method := string(c.Method())
	if method == "GET" && (path == "/" || path == "/healthz") {
		c.Next(ctx)
		return
	}
	if origin := string(c.Request.Header.Peek("Origin")); origin != "" {
		parsed, err := url.Parse(origin)
		if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host != string(c.Request.Host()) {
			c.AbortWithStatusJSON(403, map[string]string{"error": "请求来源不匹配"})
			return
		}
	}
	if s.cfg.APIToken != "" {
		header := string(c.Request.Header.Peek("Authorization"))
		token, ok := strings.CutPrefix(header, "Bearer ")
		if !ok || subtle.ConstantTimeCompare([]byte(token), []byte(s.cfg.APIToken)) != 1 {
			c.AbortWithStatusJSON(401, map[string]string{"error": "需要有效的连接令牌"})
			return
		}
	}
	c.Next(ctx)
}

func (s *Server[M]) Spin() { s.Handler().Spin() }
