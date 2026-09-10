// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package subscription

import (
	"strings"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	subscriptionv1 "github.com/PRO-Robotech/corelib/api/kacho/cloud/subscription"
	"github.com/PRO-Robotech/corelib/pagetoken"
	corevalidate "github.com/PRO-Robotech/corelib/validate"
)

// Имена осей в перечне честно отобранных — ИМЕНА ПОЛЕЙ контракта, как они там
// объявлены. Своего второго словаря осей здесь не заводится: он разошёлся бы с
// полями молча, и клиент читал бы перечень, не отвечающий ни одному полю.
const (
	axisKinds     = "kinds"
	axisProjectID = "project_id"
	axisIDs       = "ids"
)

// Filter — принятое сужение: что задал вызывающий и что владелец отобрал честно.
//
// Заданные оси сужают ВМЕСТЕ (конъюнкция); незаданная не сужает ничем — и это её
// ЕДИНСТВЕННОЕ значение.
type Filter struct {
	// Kinds — виды предметов В НАПИСАНИИ ПРОВОДА (типы объекта модели прав),
	// ровно как их назвал вызывающий. Пусто означает «все виды словаря
	// владельца».
	//
	// Слова хранилища здесь НЕТ намеренно: перевод в них принадлежит объявлению
	// журнала и делается на пути чтения ([Journal.journalWords]). Положи мы сюда
	// переведённое, принятое сужение перестало бы совпадать с тем, что назвал
	// вызывающий, — и перечень честно отобранных осей рядом говорил бы о другом.
	Kinds []string
	// ProjectID — проектный якорь. Пусто означает «проектом не сужаем».
	ProjectID string
	// IDs — идентификаторы предметов. Пусто означает «идентификатором не сужаем».
	IDs []string
	// Honored — оси, которые этот владелец отобрал ЧЕСТНО, именами полей
	// контракта. Ось, задание которой владелец принять не может, сюда не
	// попадает — она отвергает подписку ещё в [Journal.Accept].
	Honored []string
}

// Accept судит запрос подписки против объявления владельца и возвращает
// принятое сужение.
//
// # Почему отказ, а не пустой поток
//
// Пустой поток есть УТВЕРЖДЕНИЕ «событий нет», а его сервер не вправе делать про
// вход, которого он не понял. Поэтому каждый негодный вход отвергается ДО
// открытия потока, с именем поля и названным значением.
//
// # Что здесь НЕ проверяется
//
// Существование предмета. Идентификатор правильной формы, которому не отвечает
// ни один предмет, отказом НЕ является: подписка открывается и просто не
// приносит по нему событий. «Негодная форма» и «такого нет» — разные полосы, и
// подписка их не смешивает.
func (j Journal) Accept(req *subscriptionv1.SubscriptionRequest) (Filter, error) {
	var f Filter

	kinds := req.GetKinds()
	if len(kinds) > 0 {
		// Словарь берётся ТЕМ ЖЕ вызовом, каким сервер отвечает в служебном
		// сообщении открытия: объявленное клиенту и то, чем сервер судит, — один
		// объект, а не два похожих перечня.
		known := j.KindDictionary()
		index := make(map[string]struct{}, len(known))
		for _, k := range known {
			index[k] = struct{}{}
		}
		for _, kind := range kinds {
			if _, ok := index[kind]; !ok {
				// Отказ называет ГОДНЫЕ значения, а не только негодное: без них
				// единственным путём узнать словарь остаётся перебор против
				// этого самого отказа — то есть перебор по продуктовой
				// поверхности вместо чтения. Словарь есть объявление ВЛАДЕЛЬЦА,
				// не выборка по правам вызывающего, поэтому называть его здесь
				// нечем оракулить: право видеть предмет решается построчно.
				return Filter{}, status.Errorf(codes.InvalidArgument,
					"kinds: %q is not a kind of this owner; known kinds: %s",
					kind, strings.Join(known, ", "))
			}
		}
	}
	if len(kinds) > 0 {
		f.Kinds = kinds
		f.Honored = append(f.Honored, axisKinds)
	}

	for _, id := range req.GetIds() {
		// Пустая строка сюда не проходит: `corevalidate.ResourceID` её пропускает
		// по контракту (required — ответственность вызывающего), а как ось она
		// означала бы сужение, которому не отвечает ни один предмет.
		if id == "" {
			return Filter{}, status.Error(codes.InvalidArgument,
				"ids: empty identifier")
		}
		if err := corevalidate.ResourceID("resource", "", id); err != nil {
			return Filter{}, status.Errorf(codes.InvalidArgument,
				"ids: invalid resource id '%s'", id)
		}
	}
	if len(req.GetIds()) > 0 {
		f.IDs = req.GetIds()
		f.Honored = append(f.Honored, axisIDs)
	}

	if p := req.GetProjectId(); p != "" {
		// ЯКОРНАЯ ось: по ней принимается решение о показе. Владелец, не умеющий
		// по ней отобрать, ОТВЕРГАЕТ подписку, а не открывает её с оговоркой —
		// иначе границу доступа держал бы клиент.
		if j.Storage.Project == ProjectAbsent {
			return Filter{}, status.Error(codes.InvalidArgument,
				"project_id: this owner has no project dimension, so the axis cannot be honored")
		}
		f.ProjectID = p
		f.Honored = append(f.Honored, axisProjectID)
	}

	return f, nil
}

// Start — с какого места отдавать. Три состояния, и все три различимы.
type Start struct {
	// FromBeginning — с начала журнала: всё, что владелец ещё удерживает.
	FromBeginning bool
	// Position — с этой позиции. Ноль указателя означает, что позиция не
	// названа: отсутствие представимо отдельно от всякого значения, потому что
	// нулевая позиция — законная величина («ничего ещё не устоялось»).
	Position *pagetoken.SubscriptionPosition
}

// AcceptStart разбирает начало потока.
//
// ИСХОД НЕЗАДАННОГО НАЗВАН, а не подразумевается: `start` не задан означает ровно
// `CURRENT_END`. Молчаливая полная выдача журнала превратила бы подписку в
// выгрузку — тем более дорогую, чем дольше журнал не чистили, — и узнать об этом
// вызывающий мог бы только по счёту за неё.
//
// Выбрав ВЕТВЬ, вызывающий обязан её назвать: умолчание живёт у незаданной
// ветви, а не у незаданного значения внутри выбранной. Иначе одно и то же
// намерение выражалось бы двумя способами.
func AcceptStart(req *subscriptionv1.SubscriptionRequest) (Start, error) {
	switch start := req.GetStart().(type) {
	case nil:
		return Start{}, nil

	case *subscriptionv1.SubscriptionRequest_Anchor:
		switch start.Anchor {
		case subscriptionv1.SubscriptionAnchor_BEGINNING:
			return Start{FromBeginning: true}, nil
		case subscriptionv1.SubscriptionAnchor_CURRENT_END:
			return Start{}, nil
		default:
			return Start{}, status.Error(codes.InvalidArgument,
				"anchor: the anchor branch is chosen but the anchor is not named")
		}

	case *subscriptionv1.SubscriptionRequest_Position:
		if start.Position == "" {
			return Start{}, status.Error(codes.InvalidArgument,
				"position: the position branch is chosen but the position is empty")
		}
		pos, ok := pagetoken.DecodeSubscriptionPosition(start.Position)
		if !ok || pos == nil {
			// Сконструированная или изменённая клиентом позиция отвергается, а не
			// принимается «как похожая»: принятая, она начала бы поток с величины,
			// которой сервер не выдавал.
			return Start{}, status.Error(codes.InvalidArgument,
				"position: not a position issued by this server")
		}
		return Start{Position: pos}, nil

	default:
		return Start{}, status.Error(codes.InvalidArgument,
			"start: unknown branch")
	}
}
