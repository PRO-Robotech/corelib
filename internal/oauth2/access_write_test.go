// Copyright © 2024 Ory Corp
// SPDX-License-Identifier: Apache-2.0
// Изменено PRO-Robotech (modified by PRO-Robotech): перечень изменений — internal/oauth2/PROVENANCE.md.

package fosite_test

import (
	"context"
	"net/http"
	"testing"

	"github.com/golang/mock/gomock"
	"github.com/stretchr/testify/assert"

	. "github.com/PRO-Robotech/corelib/internal/oauth2"
	. "github.com/PRO-Robotech/corelib/internal/oauth2/internal"
)

func TestWriteAccessResponse(t *testing.T) {
	f := &Fosite{Config: new(Config)}
	header := http.Header{}
	ctrl := gomock.NewController(t)
	rw := NewMockResponseWriter(ctrl)
	ar := NewMockAccessRequester(ctrl)
	resp := NewMockAccessResponder(ctrl)
	defer ctrl.Finish()

	rw.EXPECT().Header().AnyTimes().Return(header)
	rw.EXPECT().WriteHeader(http.StatusOK)
	rw.EXPECT().Write(gomock.Any())
	resp.EXPECT().ToMap().Return(map[string]interface{}{})

	f.WriteAccessResponse(context.Background(), rw, ar, resp)
	assert.Equal(t, "application/json;charset=UTF-8", header.Get("Content-Type"))
	assert.Equal(t, "no-store", header.Get("Cache-Control"))
	assert.Equal(t, "no-cache", header.Get("Pragma"))
}
