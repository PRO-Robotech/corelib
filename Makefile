# Copyright (c) PRO-Robotech
# SPDX-License-Identifier: Apache-2.0
#
# Makefile фундамента — ТОЛЬКО цели провязки хуков git.
#
# Сборку, пробы и линтер ci.yml зовёт напрямую, и здесь их целей нет
# намеренно: цель `test` рядом с шагом конвейера была бы вторым изложением
# одной команды, и разошлись бы они молча. Провязка — другое дело: её не зовёт
# конвейер, а зовёт человек в своём клоне, и цель — это адрес, по которому её
# ищут (под теми же именами она есть в Makefile ствола kacho). Тела целей —
# вызовы scripts/hooks/install.sh и scripts/hooks/inject.sh, своей логики нет.
#
# Что каждая цель делает и почему переходник, а не core.hooksPath, — в шапке
# scripts/hooks/install.sh. Существование каждой цели, названной текстами хука
# и провязки, судит scripts/hooks/inject.sh.

.DEFAULT_GOAL := help
.PHONY: help install-hooks check-hooks probe-hooks

## help — перечень целей.
help:
	@sed -n 's/^## //p' $(firstword $(MAKEFILE_LIST))

## install-hooks — провязать хуки git из scripts/hooks в этот клон (один раз на клон; переходник прежней редакции перезаписывается).
install-hooks:
	@bash scripts/hooks/install.sh install

## check-hooks — сказать, провязаны ли хуки и исполнимы ли их адресаты; непровязанный клон — отказ.
check-hooks:
	@bash scripts/hooks/install.sh check

## probe-hooks — проба хука отправки и провязки инъекцией (та же, что в задании сборки ci.yml).
probe-hooks:
	@bash scripts/hooks/inject.sh
