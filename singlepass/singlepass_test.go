// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

// singlepass_test.go — отказы, которые обязаны быть ОТКАЗАМИ, а не тихим «взяли».
//
// Взаимное исключение здесь держит база, и доказывается оно интеграционными
// пробами над одной базой из двух пулов
// (services/storage/internal/reconciler/pass_claim_integration_test.go,
// services/compute/internal/repo/stuck_delete_sweep_claim_integration_test.go).
// Здесь — только то, что базы не требует: вырожденный вход.
//
// Он важнее, чем кажется. Пакет, молча отвечающий «проход твой» на неполном
// входе, вернул бы ровно тот дубль, ради которого заведён, и выглядел бы при этом
// исправно работающим.
package singlepass_test

import (
	"context"
	"errors"
	"testing"

	"github.com/PRO-Robotech/corelib/singlepass"
)

func TestTryAcquire_WithoutPoolIsRefusedNotGranted(t *testing.T) {
	release, ok, err := singlepass.TryAcquire(context.Background(), nil, "kacho.test.pass")
	if !errors.Is(err, singlepass.ErrNoPool) {
		t.Fatalf("отсутствие базы обязано быть названным отказом, получено err=%v", err)
	}
	if ok {
		t.Fatal("проход не может быть выдан без базы: выдав его, пакет разрешил бы дубль")
	}
	if release != nil {
		t.Fatal("снимать нечего — замыкание обязано быть пустым")
	}
}

func TestTryAcquire_WithoutNameIsRefused(t *testing.T) {
	// Пустое имя дало бы всем проходам ОДИН ключ: разные работы встретились бы
	// на одном замке и вытесняли друг друга — хуже, чем отсутствие развода.
	_, ok, err := singlepass.TryAcquire(context.Background(), nil, "")
	if err == nil {
		t.Fatal("пустое имя обязано быть отказом")
	}
	if ok {
		t.Fatal("проход не может быть выдан без имени")
	}
}
