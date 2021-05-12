// Copyright © 2021 Kaleido, Inc.
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

package ffresty

import (
	"context"
	"encoding/base64"
	"fmt"
	"net/http"
	"time"

	"github.com/go-resty/resty/v2"
	"github.com/kaleido-io/firefly/internal/config"
	"github.com/kaleido-io/firefly/internal/fftypes"
	"github.com/kaleido-io/firefly/internal/i18n"
	"github.com/kaleido-io/firefly/internal/log"
)

type retryCtxKey struct{}

type retryCtx struct {
	id       string
	start    time.Time
	attempts uint
}

func New(ctx context.Context, conf config.Config) *resty.Client {

	var client *resty.Client

	iHttpClient := conf.Get(HTTPCustomClient)
	if iHttpClient != nil {
		if httpClient, ok := iHttpClient.(*http.Client); ok {
			client = resty.NewWithClient(httpClient)
		}
	}
	if client == nil {
		client = resty.New()
	}

	client.OnBeforeRequest(func(c *resty.Client, req *resty.Request) error {
		rctx := req.Context()
		rc := rctx.Value(retryCtxKey{})
		if rc == nil {
			// First attempt
			r := &retryCtx{
				id:    fftypes.ShortID(),
				start: time.Now(),
			}
			rctx = context.WithValue(rctx, retryCtxKey{}, r)
			// Create a request logger from the root logger passed into the client
			l := log.L(ctx).WithField("breq", r.id)
			rctx = log.WithLogger(rctx, l)
			req.SetContext(rctx)
		}
		log.L(rctx).Infof("==> %s %s", req.Method, req.URL)
		return nil
	})

	client.OnAfterResponse(func(c *resty.Client, resp *resty.Response) error {
		rctx := resp.Request.Context()
		rc := rctx.Value(retryCtxKey{}).(*retryCtx)
		elapsed := float64(time.Since(rc.start)) / float64(time.Millisecond)
		log.L(rctx).Infof("<== %s %s [%d] (%.2fms)", resp.Request.Method, resp.Request.URL, resp.StatusCode(), elapsed)
		return nil
	})

	headers := conf.GetStringMap(HTTPConfigHeaders)
	for k, v := range headers {
		if vs, ok := v.(string); ok {
			client.SetHeader(k, vs)
		}
	}
	authUsername := conf.GetString((HTTPConfigAuthUsername))
	authPassword := conf.GetString((HTTPConfigAuthPassword))
	if authUsername != "" && authPassword != "" {
		client.SetHeader("Authorization", fmt.Sprintf("Basic %s", base64.StdEncoding.EncodeToString([]byte(fmt.Sprintf("%s:%s", authUsername, authPassword)))))
	}
	url := conf.GetString(HTTPConfigURL)
	if url != "" {
		client.SetHostURL(url)
		log.L(ctx).Debugf("Created REST client to %s", url)
	}

	if conf.GetBool(HTTPConfigRetryEnabled) {
		retryCount := conf.GetInt(HTTPConfigRetryCount)
		minTimeout := conf.GetUint(HTTPConfigRetryWaitTimeMS)
		maxTimeout := conf.GetUint(HTTPConfigRetryMaxWaitTimeMS)
		client.
			SetRetryCount(retryCount).
			SetRetryWaitTime(time.Duration(minTimeout) * time.Millisecond).
			SetRetryMaxWaitTime(time.Duration(maxTimeout) * time.Millisecond).
			AddRetryCondition(func(r *resty.Response, err error) bool {
				if r == nil || r.IsSuccess() {
					return false
				}
				rctx := r.Request.Context()
				rc := rctx.Value(retryCtxKey{}).(*retryCtx)
				log.L(rctx).Infof("retry %d/%d (min=%dms/max=%dms) status=%d", rc.attempts, retryCount, minTimeout, maxTimeout, r.StatusCode())
				rc.attempts++
				return true
			})
	}

	return client
}

func WrapRestErr(ctx context.Context, res *resty.Response, err error, key i18n.MessageKey) error {
	var respData string
	if res != nil {
		respData = res.String()
		if len(respData) > 256 {
			respData = respData[0:256] + "..."
		}
	}
	if err != nil {
		return i18n.WrapError(ctx, err, key, respData)
	}
	return i18n.NewError(ctx, key, respData)
}
