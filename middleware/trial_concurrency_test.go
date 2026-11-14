package middleware

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"testing"

	"github.com/QuantumNous/new-api/constant"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestTrialConcurrencySharedAcrossRoutesAndTokens(t *testing.T) {
	limiter := TrialConcurrencyLimit()
	r := gin.New()
	r.Use(func(c *gin.Context) {
		id, _ := strconv.Atoi(c.GetHeader("User"))
		c.Set("id", id)
		c.Set(string(constant.ContextKeyUserGroup), c.GetHeader("Group"))
	}, limiter)
	entered, release, finished := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	defer unblock()
	r.POST("/stream", func(c *gin.Context) {
		close(entered)
		<-release
		c.Status(http.StatusOK)
	})
	r.POST("/other-protocol", func(c *gin.Context) { c.Status(http.StatusOK) })
	request := func(path, user, group, token string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, path, nil)
		req.Header.Set("User", user)
		req.Header.Set("Group", group)
		req.Header.Set("Authorization", token)
		r.ServeHTTP(w, req)
		return w
	}
	go func() {
		defer close(finished)
		request("/stream", "1", "trial", "key-a")
	}()
	<-entered
	// Another key and protocol cannot bypass this account's active stream.
	require.Equal(t, 429, request("/other-protocol", "1", "trial", "key-b").Code)
	require.Equal(t, 200, request("/other-protocol", "2", "trial", "key-c").Code)
	require.Equal(t, 200, request("/other-protocol", "1", "default", "key-d").Code)
	unblock()
	<-finished
	require.Equal(t, 200, request("/other-protocol", "1", "trial", "key-b").Code)
}

func TestTrialConcurrencyReleasesOnHandlerPanic(t *testing.T) {
	r := gin.New()
	r.Use(gin.Recovery(), func(c *gin.Context) {
		c.Set("id", 11)
		c.Set(string(constant.ContextKeyUserGroup), "trial")
	}, TrialConcurrencyLimit())
	r.GET("/panic", func(c *gin.Context) { panic("upstream failure") })
	r.GET("/ok", func(c *gin.Context) { c.Status(200) })
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("GET", "/panic", nil))
	require.Equal(t, 500, w.Code)
	w = httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("GET", "/ok", nil))
	require.Equal(t, 200, w.Code)
}
