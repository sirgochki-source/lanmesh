package app

import (
	"os"
	"path/filepath"
)

// mkdirAllOwned создаёт каталог со всеми недостающими родителями и передаёт
// исходному пользователю КАЖДЫЙ созданный уровень, а не только последний.
//
// Почему недостаточно os.MkdirAll + chown конечного каталога: под sudo у
// пользователя может не быть даже ~/.config. Тогда root создаёт и его — 0700
// root:root, — и пользователь не может в него ВОЙТИ, каким бы правильным ни был
// владелец вложенного lanmesh/. Проверено на живой Ubuntu: ~/.config появился
// как drwx------ root root, и `ls ~/.config/lanmesh` падал с Permission denied,
// хотя сам lanmesh/ владельцу был назначен верно.
//
// На Windows chownToSudoUser — no-op, поэтому там функция эквивалентна MkdirAll.
func mkdirAllOwned(dir string, perm os.FileMode) error {
	// Сначала выясняем, каких уровней ещё нет: после MkdirAll отличить
	// созданные от существовавших уже не получится.
	var created []string
	for p := dir; ; {
		if _, err := os.Stat(p); err == nil {
			break // этот уровень уже есть — значит, и все выше него тоже
		} else if !os.IsNotExist(err) {
			return err
		}
		created = append(created, p)
		parent := filepath.Dir(p)
		if parent == p { // дошли до корня
			break
		}
		p = parent
	}
	if err := os.MkdirAll(dir, perm); err != nil {
		return err
	}
	for _, p := range created {
		chownToSudoUser(p)
	}
	return nil
}
