package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

func main() {
	stor := NewStorage()
	h := NewHandler(stor)
	h.Init()

	srv := &http.Server{
		Addr: addr(),
	}
	log.Fatal(srv.ListenAndServe())
}

// handlers

type Handlers struct {
	stor Stor
}

func NewHandler(stor Stor) *Handlers {
	return &Handlers{
		stor: stor,
	}
}

func (h *Handlers) Init() {
	http.HandleFunc("/", h.handle)
}

func (h *Handlers) handle(w http.ResponseWriter, r *http.Request) {
	// проверяем если метод GET
	if r.Method == http.MethodGet {
		// создаем переменную значения ответа
		var value string
		// получаем и проверяем правильность запроса
		path, err := path(r.URL.Path)
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			fmt.Fprint(w, "")
			return
		}
		// получаем, если есть query параметр timeout
		timout := r.URL.Query().Get("timeout")
		if timout != "" {
			// если таймаут задан пробуем получить значение
			t, err := strconv.Atoi(timout)
			if err != nil {
				w.WriteHeader(http.StatusBadRequest)
				fmt.Fprint(w, "")
				return
			}
			// задаем контекст
			ctx, cancel := context.WithTimeout(context.Background(), time.Duration(t)*time.Second)
			defer cancel()
			// делаем подписку на значение
			value = h.stor.Subscribe(ctx, path)
			// если в ответ пришло пустое значение, считаем что значений не было
			if value == "" {
				w.WriteHeader(http.StatusNotFound)
				fmt.Fprint(w, "")
				return
			}
		} else {
			// если таймаут не был задан, делаем запрос в хранилище
			value, err = h.stor.Get(path)
			if err != nil {
				if errors.Is(err, ErrNotFound) {
					w.WriteHeader(http.StatusNotFound)
					fmt.Fprint(w, "")
					return
				}
			}
		}
		// отправляем ответ
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, value)
		return
	}
	// если метод запроса PUT
	if r.Method == http.MethodPut {
		// получаем и проверяем params запроса
		path, err := path(r.URL.Path)
		// получаем query параметр запроса
		value := r.URL.Query().Get("v")
		// если ошибка в params или не задан query параметр v
		if err != nil || value == "" {
			w.WriteHeader(http.StatusBadRequest)
			fmt.Fprint(w, "")
			return
		}
		// добавляем новое значение в хранилище и отвечаем на запрос
		h.stor.Add(path, value)
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, "")
		return
	}
	// ответ если использовались не предусмотренные метода запроса
	w.WriteHeader(http.StatusNotImplemented)
	fmt.Fprint(w, "")
}

// path метод получения и проверки params в path запроса
func path(URLPath string) (string, error) {
	path := strings.Split(URLPath, "/")[1:]
	if len(path) != 1 {
		return "", errors.New("bad request")
	}

	return path[0], nil
}

// storage

type Storage struct {
	// repo хранилище значений
	repo map[string][]string
	// хранилище каналов
	ch map[string][]chan string
	sync.Mutex
}

type Stor interface {
	Add(name string, value string)
	Get(name string) (string, error)
	Subscribe(ctx context.Context, name string) string
}

var ErrNotFound = errors.New("not found")

// NewStorage конструктор хранилища
func NewStorage() *Storage {
	return &Storage{
		repo: map[string][]string{},
		ch:   map[string][]chan string{},
	}
}

// Add метод добавления нового значения
func (s *Storage) Add(name string, value string) {
	s.Lock()
	defer s.Unlock()
	// если есть каналы с таким именем, то значение сразу отправляем туда
	channels, ok := s.ch[name]
	// проверяем есть ли хранилище с каналам с таким именем
	// а также длинна слайса с каналами не нулевая
	if ok && len(channels) != 0 {
		// получаем первый в очереди канал
		ch := channels[0]
		// сохраняем новый слайс без первого канала
		s.ch[name] = channels[1:]
		// отправляем в канал значение
		ch <- value
		return
	}
	// если каналов нет
	// проверяем есть ли слуйс значение с таким именем
	values, ok := s.repo[name]
	if ok {
		// если есть то добавляем новое значение в конец
		s.repo[name] = append(values, value)
		return
	}
	// если слайса еще не было, то создаем новый с полученным значениеы
	s.repo[name] = []string{value}
}

// Get метод получения значения из хранилища значений
func (s *Storage) Get(name string) (string, error) {
	s.Lock()
	defer s.Unlock()
	// проверяем есть ли слайсы в мапе с таким именем
	values, ok := s.repo[name]
	if !ok || (len(values) == 0) {
		// если не нашли, то возвращаем ошибку
		return "", ErrNotFound
	}
	// забираем первое значение
	res := values[0]
	// сохраняем новый слайс без первого значения
	s.repo[name] = values[1:]
	// возвращаем результат
	return res, nil
}

// Subscribe метод получения значения по подсписке
func (s *Storage) Subscribe(ctx context.Context, name string) string {
	s.Lock()
	defer s.Unlock()
	// проверяем если в хранилище есть значение, то его и возвращаем
	val, err := s.Get(name)
	if err == nil {
		return val
	}
	// если значения не было
	// создаем канал для получения новых значений
	ch := make(chan string)
	defer close(ch)
	// ищем есть ли слайс каналов с таким именем в мапе
	channels, ok := s.ch[name]
	if ok {
		// если есть то добавляем в конец новый
		s.ch[name] = append(channels, ch)
	} else {
		// если нет, то создаем
		s.ch[name] = []chan string{ch}
	}
	// запускаем бесконечный цикл и ждем что наступит раньше
	// если раньше сработает отмена контекста, то вернем пустое значение
	// если раньше сработает созданный канал, то вернем значение полученное в нем
	for {
		select {
		case <-ctx.Done():
			return ""
		case val = <-ch:
			return val
		}
	}
}

// utils
// addr метод получения порта из аргументов командной строки
func addr() string {
	if len(os.Args) != 2 {
		log.Fatal("args port is required")
	}
	p, err := strconv.Atoi(os.Args[1])
	if err != nil {
		log.Fatal("port must be a integer")
	}

	return fmt.Sprintf("127.0.0.1:%d", p)
}
