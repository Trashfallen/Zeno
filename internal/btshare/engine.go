package btshare

// Передача больших файлов другу движком BitTorrent (anacrolix, как в
// TarBitTorrent). Раздача приватная: без DHT, трекеров и обмена пирами, о ней
// знает только друг. Друг подключается к нам напрямую (по локальной сети или
// открытому порту) или, если не выходит, через соединение pion в Zeno
// (link.go). Отдающий хеширует файл один раз; при перезапуске Zeno раздача
// поднимается без повторной проверки, загрузка продолжается с того же места.

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	g "github.com/anacrolix/generics"
	alog "github.com/anacrolix/log"
	"github.com/anacrolix/torrent"
	"github.com/anacrolix/torrent/bencode"
	"github.com/anacrolix/torrent/metainfo"
	"github.com/anacrolix/torrent/storage"
	"golang.org/x/sys/windows"
)

// FileIOEnv: без "classic" движок отображает файлы в память (mmap) и держит их
// до выхода из программы - скачанное нельзя ни перенести, ни удалить. Режим
// выбирается только переменной окружения до старта, поэтому Zeno
// перезапускает себя с ней (main.go).
const FileIOEnv = "TORRENT_STORAGE_DEFAULT_FILE_IO"

// Item - одна передача.
type Item struct {
	IH      string `json:"ih"`
	Name    string `json:"name"`
	Size    int64  `json:"size"`
	Friend  string `json:"friend"`
	Role    string `json:"role"`              // send - отдаем, get - качаем
	Path    string `json:"path"`              // send: файл или папка; get: папка загрузок
	Offered bool   `json:"offered,omitempty"` // send: предложение другу отправлено
	Added   int64  `json:"added"`

	t       *torrent.Torrent
	store   storage.ClientImplCloser // файлы держит хранилище: закрыть при удалении
	hashed  int64                    // send: сколько прохешировано
	err     string
	lastB   int64 // для скорости
	lastAt  time.Time
	speed   float64
	removed bool
}

// Status - для страницы.
type Status struct {
	Item
	State    string  `json:"state"`    // hashing, seeding, metadata, downloading, done, error
	Progress float64 `json:"progress"` // 0..1
	Speed    float64 `json:"speed"`    // байт/с: отдача у send, прием у get
	Peers    int     `json:"peers"`
	Via      string  `json:"via"` // direct, zeno или пусто
	Error    string  `json:"error,omitempty"`
}

type Engine struct {
	dir   string
	logf  func(string, ...any)
	links *links

	mu    sync.Mutex
	cl    *torrent.Client
	pc    storage.PieceCompletion
	items map[string]*Item
}

func New(dir string, logf func(string, ...any)) *Engine {
	e := &Engine{dir: dir, logf: logf, links: newLinks(), items: map[string]*Item{}}
	var list []*Item
	if b, err := os.ReadFile(filepath.Join(dir, "items.json")); err == nil {
		_ = json.Unmarshal(b, &list)
	}
	for _, it := range list {
		e.items[it.IH] = it
	}
	if len(list) > 0 {
		go func() {
			if err := e.start(); err != nil {
				e.logf("Передача: %v", err)
				return
			}
			e.mu.Lock()
			defer e.mu.Unlock()
			for _, it := range e.items {
				if err := e.activateLocked(it); err != nil {
					it.err = err.Error()
				}
			}
		}()
	}
	return e
}

// start запускает движок при первой передаче.
func (e *Engine) start() error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.cl != nil {
		return nil
	}
	if err := os.MkdirAll(filepath.Join(e.dir, "meta"), 0o755); err != nil {
		return err
	}
	pc, err := storage.NewBoltPieceCompletion(e.dir)
	if err != nil {
		return err
	}
	c := torrent.NewDefaultClientConfig()
	c.DataDir = e.dir
	// хранилище по умолчанию с той же базой кусков - иначе движок откроет ее второй раз
	c.DefaultStorage = storage.NewFileOpts(storage.NewFileClientOpts{ClientBaseDir: filepath.Join(e.dir, "default"), PieceCompletion: pc})
	c.Seed = true
	c.ListenPort = 0
	c.NoDHT = true
	c.DisableTrackers = true
	c.DisablePEX = true
	c.DisableWebtorrent = true
	c.DisableWebseeds = true
	c.NoDefaultPortForwarding = true // без UPnP: роутер не трогаем, есть путь через Zeno
	c.Logger = alog.Default.WithFilterLevel(alog.Critical)
	cl, err := torrent.NewClient(c)
	if err != nil {
		pc.Close()
		return fmt.Errorf("движок не запустился: %w", err)
	}
	cl.AddDialer(e.links)
	cl.AddListener(e.links)
	e.cl, e.pc = cl, pc
	e.logf("Передача: движок запущен, порт %d", cl.LocalPort())
	return nil
}

func (e *Engine) saveLocked() {
	list := make([]*Item, 0, len(e.items))
	for _, it := range e.items {
		if it.Role == "send" && it.t == nil && it.err == "" && !e.hasMeta(it.IH) {
			continue // еще хешируется - после перезапуска начинать заново
		}
		list = append(list, it)
	}
	b, _ := json.MarshalIndent(list, "", "  ")
	tmp := filepath.Join(e.dir, "items.json.tmp")
	if os.WriteFile(tmp, b, 0o644) == nil {
		_ = os.Rename(tmp, filepath.Join(e.dir, "items.json"))
	}
}

func (e *Engine) metaPath(ih string) string { return filepath.Join(e.dir, "meta", ih+".torrent") }

func (e *Engine) hasMeta(ih string) bool {
	_, err := os.Stat(e.metaPath(ih))
	return err == nil
}

// ---------- отдать ----------

// Share хеширует файл или папку (в фоне) и раздает другу. Предложение другу
// страница шлет, когда состояние станет seeding.
func (e *Engine) Share(friend, path string) (Status, error) {
	fi, err := os.Stat(path)
	if err != nil {
		return Status{}, err
	}
	if err := e.start(); err != nil {
		return Status{}, err
	}
	info := metainfo.Info{}
	private := true
	info.Private = &private
	size, err := walkInfo(&info, path, fi)
	if err != nil {
		return Status{}, err
	}
	// временный ключ, пока не знаем хеш раздачи
	it := &Item{IH: "hashing-" + strconv.FormatInt(time.Now().UnixNano(), 36), Name: info.Name, Size: size, Friend: friend,
		Role: "send", Path: path, Added: time.Now().Unix()}
	e.mu.Lock()
	e.items[it.IH] = it
	e.mu.Unlock()
	go e.hash(it, info)
	return e.status(it), nil
}

// walkInfo - как metainfo.BuildFromFilePath, но без хеширования.
func walkInfo(info *metainfo.Info, root string, fi os.FileInfo) (int64, error) {
	info.Name = filepath.Base(root)
	if !fi.IsDir() {
		info.Length = fi.Size()
	} else {
		err := filepath.Walk(root, func(p string, f os.FileInfo, err error) error {
			if err != nil || f.IsDir() {
				return err
			}
			rel, err := filepath.Rel(root, p)
			if err != nil {
				return err
			}
			info.Files = append(info.Files, metainfo.FileInfo{Path: strings.Split(rel, string(filepath.Separator)), Length: f.Size()})
			return nil
		})
		if err != nil {
			return 0, err
		}
		if len(info.Files) == 0 {
			return 0, errors.New("папка пустая")
		}
	}
	info.PieceLength = pieceLength(info.TotalLength())
	return info.TotalLength(), nil
}

// pieceLength: 1-4 тысячи кусков, от 256 КБ до 16 МБ.
func pieceLength(total int64) int64 {
	l := int64(256 << 10)
	for l < 16<<20 && total/l > 4000 {
		l *= 2
	}
	return l
}

type counter struct {
	r  io.ReadCloser
	it *Item
	mu *sync.Mutex
}

func (c counter) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.mu.Lock()
	c.it.hashed += int64(n)
	removed := c.it.removed
	c.mu.Unlock()
	if removed {
		return n, errors.New("передача отменена")
	}
	return n, err
}

func (c counter) Close() error { return c.r.Close() }

func (e *Engine) hash(it *Item, info metainfo.Info) {
	start := time.Now()
	err := info.GeneratePieces(func(fi metainfo.FileInfo) (io.ReadCloser, error) {
		p := it.Path
		if len(fi.Path) > 0 {
			p = filepath.Join(append([]string{it.Path}, fi.Path...)...)
		}
		f, err := os.Open(p)
		if err != nil {
			return nil, err
		}
		return counter{r: f, it: it, mu: &e.mu}, nil
	})
	var mi metainfo.MetaInfo
	if err == nil {
		mi.InfoBytes, err = bencode.Marshal(info)
	}
	if err == nil {
		mi.CreatedBy = "Zeno"
		mi.CreationDate = time.Now().Unix()
		var f *os.File
		ih := mi.HashInfoBytes().HexString()
		if f, err = os.Create(e.metaPath(ih)); err == nil {
			err = mi.Write(f)
			f.Close()
		}
		e.mu.Lock()
		old := it.IH
		delete(e.items, old)
		if !it.removed {
			if dup := e.items[ih]; dup != nil {
				err = errors.New("этот файл уже раздается")
			} else {
				it.IH = ih
				e.items[ih] = it
				err = e.activateLocked(it)
				e.saveLocked()
			}
		}
		e.mu.Unlock()
		e.logf("Передача: %q прохеширован за %v", it.Name, time.Since(start).Round(time.Second))
	}
	if err != nil {
		e.mu.Lock()
		if !it.removed {
			it.err = err.Error()
			e.items[it.IH] = it
		}
		e.mu.Unlock()
		e.logf("Передача: хеширование %q: %v", it.Name, err)
	}
}

// ---------- скачать ----------

// Get начинает загрузку раздачи друга в папку dir.
func (e *Engine) Get(friend, ih, name string, size int64, dir string) error {
	var h metainfo.Hash
	if err := h.FromHexString(ih); err != nil {
		return errors.New("кривой хеш раздачи")
	}
	if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
		return errors.New("папка для загрузки не найдена")
	}
	if free, ok := freeSpace(dir); ok && free < uint64(size) {
		return fmt.Errorf("на диске не хватит места: нужно %s, свободно %s", humanSize(uint64(size)), humanSize(free))
	}
	if err := e.start(); err != nil {
		return err
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if old := e.items[ih]; old != nil {
		return nil // уже качаем или раздаем
	}
	it := &Item{IH: ih, Name: name, Size: size, Friend: friend, Role: "get", Path: dir, Added: time.Now().Unix()}
	if err := e.activateLocked(it); err != nil {
		return err
	}
	e.items[ih] = it
	e.saveLocked()
	return nil
}

// activateLocked добавляет передачу в движок.
func (e *Engine) activateLocked(it *Item) error {
	if e.cl == nil || it.t != nil {
		return nil
	}
	var spec *torrent.TorrentSpec
	if mi, err := metainfo.LoadFromFile(e.metaPath(it.IH)); err == nil {
		if spec, err = torrent.TorrentSpecFromMetaInfoErr(mi); err != nil {
			return err
		}
	} else if it.Role == "get" {
		var h metainfo.Hash
		if err := h.FromHexString(it.IH); err != nil {
			return err
		}
		spec = &torrent.TorrentSpec{DisplayName: it.Name}
		spec.InfoHash = h
	} else {
		return errors.New("нет данных раздачи, отправьте файл заново")
	}
	if it.Role == "send" {
		base, name := filepath.Dir(it.Path), filepath.Base(it.Path)
		it.store = storage.NewFileOpts(storage.NewFileClientOpts{
			ClientBaseDir:   base,
			PieceCompletion: allComplete{}, // файл свой и только что прохеширован
			UsePartFiles:    g.Some(false),
			FilePathMaker: func(o storage.FilePathMakerOpts) string {
				return filepath.Join(append([]string{name}, o.File.BestPath()...)...)
			},
		})
	} else {
		it.store = storage.NewFileOpts(storage.NewFileClientOpts{
			ClientBaseDir:   it.Path,
			PieceCompletion: sharedPC{e.pc}, // хранилище закрывает свою базу, а эта общая
			FilePathMaker: func(o storage.FilePathMakerOpts) string {
				return safeFilePath(o.Info, o.File)
			},
		})
	}
	spec.Storage = it.store
	t, _, err := e.cl.AddTorrentSpec(spec)
	if err != nil {
		it.store.Close()
		it.store = nil
		return err
	}
	it.t = t
	t.AddPeers([]torrent.PeerInfo{{Addr: zAddr(it.Friend), Trusted: true}})
	if it.Role == "get" {
		go func() {
			select {
			case <-t.GotInfo():
			case <-t.Closed():
				return
			}
			t.DownloadAll()
			// метаданные - на диск: после перезапуска не ждать отдающего
			if f, err := os.Create(e.metaPath(it.IH)); err == nil {
				mi := t.Metainfo()
				_ = mi.Write(f)
				f.Close()
			}
		}()
	}
	return nil
}

// AddPeers - прямые адреса друга для раздачи ih (ip:порт).
func (e *Engine) AddPeers(ih string, addrs []string) {
	e.mu.Lock()
	it := e.items[ih]
	e.mu.Unlock()
	if it == nil || it.t == nil {
		return
	}
	var peers []torrent.PeerInfo
	for _, a := range addrs {
		if ap, err := net.ResolveTCPAddr("tcp4", a); err == nil && ap.IP != nil {
			peers = append(peers, torrent.PeerInfo{Addr: ipPortAddr{ap.IP, ap.Port}, Trusted: true})
		}
	}
	it.t.AddPeers(peers)
}

type ipPortAddr struct {
	ip   net.IP
	port int
}

func (a ipPortAddr) String() string { return net.JoinHostPort(a.ip.String(), strconv.Itoa(a.port)) }

// Addrs - наши прямые адреса для друга: локальные IPv4 и внешний, с портом движка.
func (e *Engine) Addrs(public string) []string {
	e.mu.Lock()
	cl := e.cl
	e.mu.Unlock()
	if cl == nil {
		return nil
	}
	port := strconv.Itoa(cl.LocalPort())
	var out []string
	if ifs, err := net.InterfaceAddrs(); err == nil {
		for _, a := range ifs {
			if n, ok := a.(*net.IPNet); ok && n.IP.To4() != nil && !n.IP.IsLoopback() && !n.IP.IsLinkLocalUnicast() {
				out = append(out, net.JoinHostPort(n.IP.String(), port))
			}
		}
	}
	if ip := net.ParseIP(public); ip != nil && ip.To4() != nil {
		out = append(out, net.JoinHostPort(ip.String(), port))
	}
	return out
}

// Location - где лежит файл или папка передачи (для «показать в папке»).
func (e *Engine) Location(ih string) string {
	e.mu.Lock()
	defer e.mu.Unlock()
	it := e.items[ih]
	switch {
	case it == nil:
		return ""
	case it.Role == "send":
		return it.Path
	}
	p := filepath.Join(it.Path, safeName(it.Name))
	if _, err := os.Stat(p); err != nil {
		if _, err := os.Stat(p + ".part"); err == nil {
			return p + ".part"
		}
		return it.Path
	}
	return p
}

// Remove убирает передачу (файлы на диске остаются).
func (e *Engine) Remove(ih string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	it := e.items[ih]
	if it == nil {
		return
	}
	it.removed = true
	delete(e.items, ih)
	if it.t != nil {
		it.t.Drop()
		it.t = nil
	}
	if it.store != nil {
		it.store.Close()
		it.store = nil
	}
	_ = os.Remove(e.metaPath(ih))
	e.saveLocked()
}

// MarkOffered: предложение другу ушло.
func (e *Engine) MarkOffered(ih string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if it := e.items[ih]; it != nil && !it.Offered {
		it.Offered = true
		e.saveLocked()
	}
}

// List - передачи с другом (пустой friend - все).
func (e *Engine) List(friend string) []Status {
	e.mu.Lock()
	items := make([]*Item, 0, len(e.items))
	for _, it := range e.items {
		if friend == "" || it.Friend == friend {
			items = append(items, it)
		}
	}
	e.mu.Unlock()
	out := make([]Status, 0, len(items))
	for _, it := range items {
		out = append(out, e.status(it))
	}
	return out
}

func (e *Engine) status(it *Item) Status {
	e.mu.Lock()
	defer e.mu.Unlock()
	st := Status{Item: *it, Error: it.err}
	st.t = nil
	t := it.t
	switch {
	case it.err != "":
		st.State = "error"
	case t == nil && it.Role == "send":
		st.State = "hashing"
		if it.Size > 0 {
			st.Progress = float64(it.hashed) / float64(it.Size)
		}
	case t == nil:
		st.State = "error"
		st.Error = "движок не запущен"
	}
	if t == nil || st.State != "" {
		return st
	}
	stats := t.Stats()
	st.Peers = stats.ActivePeers
	for _, pc := range t.PeerConns() {
		if strings.HasPrefix(pc.RemoteAddr.String(), addrPrefix) {
			st.Via = "zeno"
		} else if st.Via == "" {
			st.Via = "direct"
		}
	}
	bytes := stats.BytesReadData.Int64()
	if it.Role == "send" {
		bytes = stats.BytesWrittenData.Int64()
	}
	now := time.Now()
	if !it.lastAt.IsZero() && now.Sub(it.lastAt) > 500*time.Millisecond {
		v := float64(bytes-it.lastB) / now.Sub(it.lastAt).Seconds()
		it.speed = it.speed*0.5 + v*0.5
	}
	if it.lastAt.IsZero() || now.Sub(it.lastAt) > 500*time.Millisecond {
		it.lastB, it.lastAt = bytes, now
	}
	st.Speed = it.speed
	select {
	case <-t.GotInfo():
		total := t.Length()
		if total > 0 {
			st.Progress = float64(t.BytesCompleted()) / float64(total)
		}
		switch {
		case it.Role == "send":
			st.State = "seeding"
		case t.BytesMissing() == 0:
			st.State = "done"
		default:
			st.State = "downloading"
		}
	default:
		st.State = "metadata"
	}
	return st
}

// ---------- соединение с другом (страница передает SDP) ----------

func (e *Engine) LinkOffer(friend string, ice []ICEServer) (string, error) {
	return e.links.offer(friend, ice)
}

func (e *Engine) LinkAccept(friend, sdp string, ice []ICEServer) (string, error) {
	return e.links.acceptOffer(friend, sdp, ice)
}

func (e *Engine) LinkAnswer(friend, sdp string) error { return e.links.answer(friend, sdp) }
func (e *Engine) LinkState(friend string) string      { return e.links.state(friend) }
func (e *Engine) LinkClose(friend string)             { e.links.close(friend) }

// Stop - выход из Zeno.
func (e *Engine) Stop() {
	e.links.stop()
	e.mu.Lock()
	cl, pc := e.cl, e.pc
	e.cl, e.pc = nil, nil
	var stores []storage.ClientImplCloser
	for _, it := range e.items {
		it.t = nil
		if it.store != nil {
			stores = append(stores, it.store)
			it.store = nil
		}
	}
	e.mu.Unlock()
	if cl != nil {
		cl.Close()
	}
	for _, s := range stores {
		s.Close()
	}
	if pc != nil {
		pc.Close()
	}
}

// ---------- хранилище ----------

type sharedPC struct{ storage.PieceCompletion }

func (sharedPC) Close() error { return nil }

// allComplete: у отдающего все куски на месте - движок их не перепроверяет.
type allComplete struct{}

func (allComplete) Get(metainfo.PieceKey) (storage.Completion, error) {
	return storage.Completion{Complete: true, Ok: true}, nil
}
func (allComplete) Set(metainfo.PieceKey, bool) error { return nil }
func (allComplete) Close() error                      { return nil }

// safeFilePath - путь файла от друга внутри папки загрузок: без ".." и
// запрещенных в Windows символов.
func safeFilePath(info *metainfo.Info, f *metainfo.FileInfo) string {
	parts := []string{safeName(info.BestName())}
	for _, s := range f.BestPath() {
		if s != "" && s != "." {
			parts = append(parts, safeName(s))
		}
	}
	return filepath.Join(parts...)
}

func safeName(name string) string {
	name = strings.Map(func(r rune) rune {
		if r < 32 || strings.ContainsRune(`<>:"/\|?*`, r) {
			return '_'
		}
		return r
	}, name)
	name = strings.TrimRight(name, ". ")
	if name == "" {
		return "_"
	}
	return name
}

func freeSpace(dir string) (uint64, bool) {
	p, err := windows.UTF16PtrFromString(dir)
	if err != nil {
		return 0, false
	}
	var free uint64
	if windows.GetDiskFreeSpaceEx(p, &free, nil, nil) != nil {
		return 0, false
	}
	return free, true
}

func humanSize(n uint64) string {
	switch {
	case n >= 1<<30:
		return fmt.Sprintf("%.1f ГБ", float64(n)/(1<<30))
	case n >= 1<<20:
		return fmt.Sprintf("%.0f МБ", float64(n)/(1<<20))
	}
	return fmt.Sprintf("%d КБ", n>>10)
}
