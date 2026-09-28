// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

// engineconfig_capacity_internal_test.go — срез в настройках движка, собранных
// New, не отдаёт одновременным обменам незанятой части своего массива.
//
// Настройки делят все обмены. У среза с запасом ёмкости (ёмкость больше длины)
// с ними делится и незанятая часть массива: `append` к такому срезу на пути
// запроса пишет в общий массив, и два обмена, дописывающие одновременно, пишут
// в один элемент — каждый может получить в своём результате чужой. Элементы до
// длины при этом не меняются, и сверка настроек по значению
// (TestEngineSettingsBuiltByNewAreOnlyReadOnTheRequestPath) такую запись не
// видит. Дописывание к срезу настроек на пути запроса есть: Request.Sanitize
// движка дописывает общие поля к SanitationWhiteList
// (`append(allowedParameters, defaultAllowedParameters...)`). Срез без запаса
// ёмкости такой `append` копирует, и общего массива обмены не пишут никогда —
// какой бы код ни дописывал к срезу настроек.
//
// Файл внутренний намеренно: настройки движка не видны снаружи ни одним
// элементом пакета.
package oauthceremony

import (
	"reflect"
	"strconv"
	"strings"
	"testing"

	engine "github.com/PRO-Robotech/corelib/internal/oauth2"
)

// sliceCensus — срезы настроек, осмотренные обходом, и те из них, у которых
// есть запас ёмкости.
type sliceCensus struct {
	// inspected — полей-срезов осмотрено, вложенные срезы в счёт.
	inspected int
	// nonEmpty — из них непустых: обход, не встретивший ни одного непустого
	// среза, судил не настройки, собранные New.
	nonEmpty int
	// spare — путь к срезу с запасом и его длина с ёмкостью.
	spare []string
}

// censusSpareCapacity обходит каждое поле-срез настроек cfg — и срезы,
// лежащие элементами в срезе срезов, — и называет каждый с запасом ёмкости.
// Указатели и интерфейсы обход не раскрывает: за ними не содержимое настроек, а
// обработчики и стратегии со своим состоянием.
func censusSpareCapacity(cfg *engine.Config) sliceCensus {
	var c sliceCensus
	v := reflect.ValueOf(cfg).Elem()
	for i := range v.NumField() {
		if v.Field(i).Kind() == reflect.Slice {
			c.slice(v.Type().Field(i).Name, v.Field(i))
		}
	}
	return c
}

func (c *sliceCensus) slice(path string, v reflect.Value) {
	c.inspected++
	if v.Len() > 0 {
		c.nonEmpty++
	}
	if v.Cap() > v.Len() {
		c.spare = append(c.spare, path+" (длина "+strconv.Itoa(v.Len())+", ёмкость "+strconv.Itoa(v.Cap())+")")
	}
	if v.Type().Elem().Kind() != reflect.Slice {
		return
	}
	for i := range v.Len() {
		c.slice(path+"["+strconv.Itoa(i)+"]", v.Index(i))
	}
}

// requireSliceCensusCovered — обход осмотрел непустое: пустой обход не вердикт.
func requireSliceCensusCovered(t *testing.T, c sliceCensus) {
	t.Helper()

	t.Logf("перепись: полей-срезов осмотрено %d · непустых %d · с запасом ёмкости %d", c.inspected, c.nonEmpty, len(c.spare))
	if c.inspected == 0 || c.nonEmpty == 0 {
		t.Fatalf("НЕ ВЫПОЛНИЛОСЬ: полей-срезов осмотрено %d, непустых %d — судить нечего", c.inspected, c.nonEmpty)
	}
}

// TestEngineSettingsSlicesBuiltByNewHaveNoSpareCapacity — ни один срез
// настроек движка собранной церемонии не держит запаса ёмкости.
func TestEngineSettingsSlicesBuiltByNewHaveNoSpareCapacity(t *testing.T) {
	census := censusSpareCapacity(engineConfigOfNewCeremony(t))
	requireSliceCensusCovered(t, census)

	for _, at := range census.spare {
		t.Errorf("срез настроек движка %s держит запас ёмкости: append к нему на пути запроса "+
			"пишет в массив, общий для одновременных обменов", at)
	}
}

// TestSpareCapacityInjectionIsNamed — обход краснеет на поле-срезе с запасом
// ёмкости и называет его, а на законном близнеце — том же содержимом без
// запаса — молчит. Инъекция роняет ровно одно поле: прочие настройки те, что
// собрал New.
func TestSpareCapacityInjectionIsNamed(t *testing.T) {
	for _, tc := range []struct {
		name  string
		spare bool
	}{
		{name: "запас ёмкости", spare: true},
		{name: "близнец без запаса"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := engineConfigOfNewCeremony(t)
			whitelist := make([]string, len(cfg.SanitationWhiteList), len(cfg.SanitationWhiteList)+1)
			if !tc.spare {
				whitelist = whitelist[:len(whitelist):len(whitelist)]
			}
			copy(whitelist, cfg.SanitationWhiteList)
			cfg.SanitationWhiteList = whitelist

			census := censusSpareCapacity(cfg)
			requireSliceCensusCovered(t, census)

			named := 0
			for _, at := range census.spare {
				if strings.HasPrefix(at, "SanitationWhiteList ") {
					named++
				}
			}
			switch {
			case tc.spare && named != 1:
				t.Errorf("инъекция запаса в SanitationWhiteList не названа: находки %q", census.spare)
			case !tc.spare && named != 0:
				t.Errorf("близнец без запаса назван находкой: %q", census.spare)
			}
		})
	}
}
