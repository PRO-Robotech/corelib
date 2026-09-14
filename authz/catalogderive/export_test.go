// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package catalogderive

// export_test.go — крючок ДЛЯ ИНЪЕКЦИИ, доступный только пробам этого пакета.
//
// Он существует ровно затем, чтобы инъекция подавалась НАСТОЯЩЕМУ читателю
// (`AnnotationsOf`), а не его копии. Проба, воспроизводящая логику чтения у
// себя, доказывала бы работоспособность своей копии и молчала бы о том, что
// проверяет.
//
// Файл `_test.go`, поэтому в поставляемое двоичное не попадает ни при какой
// сборке: крючок недостижим из прод-кода by construction, а не по соглашению.

// SetFoundationLaneForTest подставляет запись перечня полос и возвращает
// функцию отката.
//
// Откат обязателен и возвращается ВМЕСТО параметра `t`: перечень — состояние
// процесса, и проба, забывшая его вернуть, отравила бы соседние пробы того же
// двоичного. Возврат функции делает забывчивость видимой в месте вызова.
func SetFoundationLaneForTest(fullMethod string, lane FoundationLane) (restore func()) {
	prev, had := foundationLanes[fullMethod]
	foundationLanes[fullMethod] = lane
	return func() {
		if had {
			foundationLanes[fullMethod] = prev
			return
		}
		delete(foundationLanes, fullMethod)
	}
}

// UseVocabularyPackageForTest подменяет имя пакета словаря и СБРАСЫВАЕТ кэш
// резолва, возвращая функцию отката.
//
// Существует ради одного доказательства: «словарь не приехал» обязано давать
// ИМЕНОВАННЫЙ отказ, а не пустую разметку. Проверить это на копии читателя
// нельзя — копия доказала бы себя, — поэтому подменяется вход НАСТОЯЩЕГО
// читателя.
//
// Кэш сбрасывается ДВАЖДЫ (на подмене и на откате): `sync.OnceValues` помнит
// первый ответ, и проба, забывшая сброс, увидела бы у соседней пробы того же
// двоичного словарь, которого уже нет, — либо его отсутствие там, где он есть.
func UseVocabularyPackageForTest(pkg string) (restore func()) {
	prevPkg := vocabularyPackage
	vocabularyPackage = pkg
	loadVocabulary = newVocabularyLoader()
	return func() {
		vocabularyPackage = prevPkg
		loadVocabulary = newVocabularyLoader()
	}
}
