package store

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

const (
	ScopeGlobal = "global"

	dateLayout = "2006-01-02"
)

// Memory is one durable claim an identity holds. The claim is the document
// body; everything else says how far to trust it and where it applies.
type Memory struct {
	ID      string    `yaml:"id"`
	Kind    string    `yaml:"kind"`
	Scope   string    `yaml:"scope"`
	Source  string    `yaml:"source"`
	Created time.Time `yaml:"created"`
	Session string    `yaml:"session,omitempty"`
	Anchor  string    `yaml:"anchor,omitempty"`
	Tags    []string  `yaml:"tags,omitempty,flow"`
	Body    string    `yaml:"-"`
	Path    string    `yaml:"-"` // where the memory was loaded from
}

// MemoryFile identifies a memory on disk and the state it was last seen in.
type MemoryFile struct {
	Name    string
	Path    string
	Size    int64
	ModTime time.Time
}

func (s *Store) memoryDir(identity string) string {
	return filepath.Join(s.IdentityDir(identity), "memories")
}

// AddMemory writes a new memory and returns it with its id assigned.
func (s *Store) AddMemory(identity string, m Memory) (Memory, error) {
	dir := s.memoryDir(identity)
	m.Created = m.Created.UTC().Truncate(time.Second)
	id, err := unusedID(dir)
	if err != nil {
		return Memory{}, err
	}
	m.ID = id
	doc, err := encodeDoc(m, m.Body)
	if err != nil {
		return Memory{}, err
	}
	name := m.Created.Format(dateLayout) + "-" + m.ID + ".md"
	return m, writeFile(filepath.Join(dir, name), doc)
}

// Memory finds one memory by its exact id, including outside the current scope.
func (s *Store) Memory(identity, id string) (Memory, error) {
	files, err := s.MemoryFiles(identity)
	if err != nil {
		return Memory{}, err
	}
	for _, file := range files {
		m, err := LoadMemory(file)
		if err != nil {
			return Memory{}, err
		}
		if m.ID == id {
			return m, nil
		}
	}
	return Memory{}, fmt.Errorf("memory %q: %w", id, ErrNotFound)
}

// EditMemory replaces the claim, preserving its id, path and metadata.
func (s *Store) EditMemory(identity, id, body string) error {
	m, err := s.Memory(identity, id)
	if err != nil {
		return err
	}
	doc, err := encodeDoc(m, body)
	if err != nil {
		return err
	}
	return writeFile(m.Path, doc)
}

// RemoveMemory permanently deletes one memory's source file.
func (s *Store) RemoveMemory(identity, id string) error {
	m, err := s.Memory(identity, id)
	if err != nil {
		return err
	}
	return os.Remove(m.Path)
}

// unusedID picks an id no record in dir already carries.
func unusedID(dir string) (string, error) {
	for {
		id := newID(8)
		taken, err := filepath.Glob(filepath.Join(dir, "*"+id+"*"))
		if err != nil {
			return "", err
		}
		if len(taken) == 0 {
			return id, nil
		}
	}
}

// MemoryFiles lists the identity's memory files, oldest name first.
func (s *Store) MemoryFiles(identity string) ([]MemoryFile, error) {
	dir := s.memoryDir(identity)
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var files []MemoryFile
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || strings.HasPrefix(name, ".") || !strings.HasSuffix(name, ".md") {
			continue
		}
		info, err := entry.Info()
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, err
		}
		files = append(files, MemoryFile{
			Name:    name,
			Path:    filepath.Join(dir, name),
			Size:    info.Size(),
			ModTime: info.ModTime(),
		})
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Name < files[j].Name })
	return files, nil
}

var datePrefix = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}-`)

// LoadMemory reads one memory file. Missing frontmatter fields get defaults,
// so a file the user wrote by hand is a memory the user told the identity.
func LoadMemory(file MemoryFile) (Memory, error) {
	var m Memory
	body, err := readDoc(file.Path, &m)
	if err != nil {
		return Memory{}, err
	}
	m.Body = body
	m.Path = file.Path
	if m.ID == "" {
		m.ID = datePrefix.ReplaceAllString(strings.TrimSuffix(file.Name, ".md"), "")
	}
	if m.Kind == "" {
		m.Kind = "fact"
	}
	if m.Scope == "" {
		m.Scope = ScopeGlobal
	}
	if m.Source == "" {
		m.Source = "told"
	}
	if m.Created.IsZero() {
		m.Created = file.ModTime.UTC().Truncate(time.Second)
	}
	return m, nil
}

// Memories loads every memory the identity holds, oldest file first. Files
// that cannot be read are left out and reported in skipped.
func (s *Store) Memories(identity string) (memories []Memory, skipped []error, err error) {
	files, err := s.MemoryFiles(identity)
	if err != nil {
		return nil, nil, err
	}
	for _, file := range files {
		m, err := LoadMemory(file)
		if err != nil {
			skipped = append(skipped, err)
			continue
		}
		memories = append(memories, m)
	}
	return memories, skipped, nil
}
