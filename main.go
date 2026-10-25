package main

import (
	"archive/zip"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

type Chunk struct {
	ID       string   `json:"id"`
	Title    string   `json:"title"`
	Citation string   `json:"citation"`
	Text     string   `json:"text"`
	Keywords []string `json:"keywords"`
}
type ChatRequest struct {
	Message string                           `json:"message"`
	History []struct{ Role, Content string } `json:"history,omitempty"`
}
type ChatResponse struct {
	Answer     string   `json:"answer"`
	Sources    []string `json:"sources"`
	Matches    []Match  `json:"matches,omitempty"`
	Disclaimer string   `json:"disclaimer,omitempty"`
}
type Match struct {
	Title    string `json:"title"`
	Citation string `json:"citation"`
	Score    int    `json:"score"`
	Excerpt  string `json:"excerpt"`
}

var chunks []Chunk
var wordRE = regexp.MustCompile(`[\p{L}\p{N}]+`)

func main() {
	if err := loadKnowledge(); err != nil {
		log.Fatalf("knowledge load: %v", err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/api/health", func(w http.ResponseWriter, r *http.Request) {
		jsonOut(w, 200, map[string]any{"status": "ok", "app": "VisariumAI", "knowledge_chunks": len(chunks), "mode": "local-grounded-search"})
	})
	mux.HandleFunc("/api/chat", chatHandler)
	mux.HandleFunc("/api/knowledge", knowledgeHandler)
	mux.HandleFunc("/api/templates", templatesHandler)
	mux.HandleFunc("/api/check", checkHandler)
	mux.Handle("/", http.FileServer(http.Dir("web")))
	addr := os.Getenv("VISARIUM_ADDR")
	if addr == "" {
		addr = ":8080"
	}
	srv := &http.Server{Addr: addr, Handler: securityHeaders(mux), ReadHeaderTimeout: 5 * time.Second}
	log.Printf("VisariumAI listening on %s; knowledge chunks=%d", addr, len(chunks))
	log.Fatal(srv.ListenAndServe())
}
func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "SAMEORIGIN")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Content-Security-Policy", "default-src 'self' 'unsafe-inline' 'unsafe-eval' https: data: blob:; connect-src 'self'")
		next.ServeHTTP(w, r)
	})
}
func jsonOut(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
func loadKnowledge() error {
	b, e := os.ReadFile("knowledge/knowledge.json")
	if e != nil {
		return e
	}
	return json.Unmarshal(b, &chunks)
}
func chatHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		jsonOut(w, 405, map[string]string{"error": "Используйте POST"})
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	var req ChatRequest
	if e := json.NewDecoder(r.Body).Decode(&req); e != nil {
		jsonOut(w, 400, map[string]string{"error": "Некорректный JSON"})
		return
	}
	q := strings.TrimSpace(req.Message)
	if q == "" {
		jsonOut(w, 400, map[string]string{"error": "Введите вопрос"})
		return
	}
	if len([]rune(q)) > 4000 {
		jsonOut(w, 413, map[string]string{"error": "Вопрос слишком длинный (максимум 4000 символов)"})
		return
	}
	matches := search(q, 5)
	answer, sources := answerFor(q, matches)
	jsonOut(w, 200, ChatResponse{Answer: answer, Sources: sources, Matches: matches, Disclaimer: "Ответ сформирован по локальным учебным материалам. Перед применением проверьте актуальную редакцию документа и внутренние правила организации."})
}
func search(q string, limit int) []Match {
	q = strings.ToLower(q)
	tokens := wordRE.FindAllString(q, -1)
	stop := map[string]bool{"что": true, "как": true, "где": true, "для": true, "при": true, "или": true, "это": true, "мне": true, "надо": true, "можно": true, "должен": true, "должны": true, "есть": true, "про": true, "кто": true, "какие": true, "какой": true, "какая": true, "нужно": true, "будет": true, "документ": true}
	out := []Match{}
	for _, c := range chunks {
		score := 0
		hay := strings.ToLower(c.Title + " " + c.Citation + " " + c.Text + " " + strings.Join(c.Keywords, " "))
		for _, t := range tokens {
			if len([]rune(t)) < 3 || stop[t] {
				continue
			}
			if strings.Contains(hay, t) {
				score += 2
			}
			for _, kw := range c.Keywords {
				if strings.Contains(strings.ToLower(kw), t) {
					score += 2
					break
				}
			}
		}
		if score > 0 {
			excerpt := c.Text
			if len([]rune(excerpt)) > 340 {
				excerpt = string([]rune(excerpt)[:340]) + "…"
			}
			out = append(out, Match{Title: c.Title, Citation: c.Citation, Score: score, Excerpt: excerpt})
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Score > out[j].Score })
	if len(out) > limit {
		out = out[:limit]
	}
	return out
}
func answerFor(q string, m []Match) (string, []string) {
	l := strings.ToLower(q)
	sources := []string{}
	for _, x := range m {
		sources = append(sources, x.Citation)
	}
	if has(l, "модул", "шаблон", "актуальн", "редакц", "верси") {
		return "Перед применением модульного документа проверьте в реестре его наименование, регистрационные данные, дату, редакцию (версию), владельца и начало применения. Используйте только модуль с актуальным статусом. В регистрационной карточке проекта укажите факт применения модуля и его реквизиты; свяжите карточку проекта с карточкой модуля.\n\nЕсли проект строго соответствует утверждённому модулю, повторное полное согласование подразделениями, согласовавшими модуль, не проводится, однако обязательные процедуры, включая лингвистическую экспертизу и визирование ДОУ, сохраняются. Изменение постоянных формулировок, обязательных пунктов или смысла решения означает, что проект отличается от модуля и требует полного согласования.\n\nВ предоставленных материалах нет реестра с текущими карточками модулей, поэтому подтвердить актуальность конкретного модуля здесь нельзя.", unique(append(sources, "Методические рекомендации по работе с модульными документами · пп. 1.2–1.3, 2.3–2.4"))
	}
	if has(l, "ввод", "эксплуатац", "информационн", "ис ") {
		return "Для приказа по информационной системе Матрица обязательного визирования указывает обязательные визы юридического подразделения и Департамента ДОУ. В строке о приказах по вопросам информационных систем также отдельно указаны Департамент информационной безопасности и Департамент информационных технологий. Учитывайте их в маршруте по содержанию проекта и компетенциям затронутых подразделений.\n\nПо Инструкции сначала формируется и согласуется проект, затем проводятся обязательные визирование и экспертизы, после чего документ направляется уполномоченному лицу на подписание и далее на регистрацию. Точный маршрут следует проверить по действующей Матрице и полной Инструкции.", unique(append(sources, "Матрица обязательного визирования · строка 3 (приказы по вопросам информационных систем)", "Инструкция по ДОУ · глава 10"))
	}
	if has(l, "согласующ", "визир", "маршрут", "матриц") {
		if len(m) == 0 {
			return fallback()
		}
		return "Матрица применяется дополнительно к общему маршруту визирования. Если проект подпадает под несколько строк, требования соответствующих строк применяются совокупно. Подразделение, чья компетенция затрагивается проектом, включается в маршрут независимо от отдельной отметки в Матрице. Отметка «Да» означает обязательное визирование, «Нет» — отсутствие обязательного визирования по Матрице, «При условии» — визирование при наличии указанного условия.\n\nОпределите вид документа и специальные условия, найдите соответствующие строки Матрицы, объедините применимые требования и затем сверьте маршрут с общей процедурой Инструкции.", unique(append(sources, "Матрица обязательного визирования · пп. 1–3"))
	}
	if has(l, "регистрац", "подпис", "согласован", "срок рассмотр") {
		return "Общий процесс включает подготовку проекта, внутреннее согласование, рассмотрение заинтересованными подразделениями, обязательное визирование и экспертизы, направление уполномоченному должностному лицу на подписание и последующую регистрацию. Регистрационный номер подписываемого документа до подписания и направления на регистрацию не присваивается. Завизированная и подписываемая версии должны совпадать; существенные изменения после визирования требуют повторного рассмотрения.\n\nДля точного срока рассмотрения укажите вид документа и этап процесса: срок зависит от категории документа и применимого пункта Инструкции.", unique(append(sources, "Методические рекомендации по работе с модульными документами · пп. 2.1, 4.3, 5.1–5.3"))
	}
	if has(l, "передач", "имуще", "прием", "приём") {
		return "Для приказа о приёме и передаче дел и имущества укажите основание, лицо или подразделение, сдающее дела, принимающую сторону, состав передаваемых документов и имущества, срок, представителя организации и лицо, контролирующее исполнение. Если перечень велик, материалы можно перечислить в акте приёма-передачи.\n\nЕсли передача связана с предоставлением, изменением или прекращением доступа к информационным системам, Матрица предусматривает визирование Департаментом информационной безопасности.", unique(append(sources, "Модуль приказа о приёме и передаче дел и имущества · примечания 1–8", "Матрица обязательного визирования · строка 2"))
	}
	if has(l, "рабочую группу", "рабочей групп", "проектного комитета", "комитет") {
		return "Для распоряжения о создании рабочей группы определите практическую цель, единообразно сформулируйте предмет работы в заголовке и пункте 1, укажите состав с ролями и официальными должностями, конечный срок, конкретные действия и ожидаемый результат с адресатом. Укажите событие завершения деятельности группы и должностное лицо, контролирующее исполнение. Не оставляйте линии и номера примечаний в подписываемом документе.\n\nПо Матрице распоряжения о создании рабочих групп и проектных комитетов требуют визирования Департаментом ДОУ; Департамент информационных технологий визирует проект, если временное объединение создаётся для реализации ИТ-проекта или решения вопросов информационных систем.", unique(append(sources, "Модуль распоряжения о создании рабочей группы · примечания 1–8", "Матрица обязательного визирования · строка 6"))
	}
	if len(m) == 0 || m[0].Score < 2 {
		return fallback()
	}
	var b strings.Builder
	b.WriteString("Нашёл следующие положения в предоставленных материалах.\n\n")
	for i, x := range m {
		if i == 3 {
			break
		}
		fmt.Fprintf(&b, "**%s** (%s): %s\n\n", x.Title, x.Citation, x.Excerpt)
	}
	b.WriteString("Это извлечение из учебных источников, а не подтверждение актуального статуса документа. Если уточните тип документа и действие, смогу сузить ответ.")
	return b.String(), sources
}
func fallback() (string, []string) {
	return "В доступной базе знаний не нашлось достаточно подтверждённых сведений для уверенного ответа. Я не буду придумывать требование. Уточните, пожалуйста, вид документа (приказ, распоряжение, письмо), действие (подготовка, согласование, визирование, подписание или регистрация) и, если известно, пункт Инструкции. Также можно обратиться к ответственному подразделению ДОУ.", []string{"База знаний VisariumAI · недостаточно подтверждённых данных"}
}
func has(s string, terms ...string) bool {
	for _, t := range terms {
		if strings.Contains(s, t) {
			return true
		}
	}
	return false
}
func unique(a []string) []string {
	seen := map[string]bool{}
	o := []string{}
	for _, s := range a {
		if s != "" && !seen[s] {
			seen[s] = true
			o = append(o, s)
		}
	}
	return o
}
func knowledgeHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		jsonOut(w, 405, map[string]string{"error": "Используйте GET"})
		return
	}
	out := make([]map[string]string, 0, len(chunks))
	for _, c := range chunks {
		out = append(out, map[string]string{"title": c.Title, "citation": c.Citation, "text": c.Text})
	}
	jsonOut(w, 200, map[string]any{"items": out, "count": len(out)})
}
func templatesHandler(w http.ResponseWriter, r *http.Request) {
	jsonOut(w, 200, map[string]any{"templates": []map[string]string{{"id": "group", "name": "Распоряжение о создании рабочей группы"}, {"id": "committee", "name": "Распоряжение о создании проектного комитета"}, {"id": "audit", "name": "Распоряжение о проведении внутреннего аудита"}, {"id": "regulation", "name": "Приказ об утверждении локального документа"}, {"id": "handover", "name": "Приказ о приёме и передаче дел и имущества"}, {"id": "docflow", "name": "Приказ о назначении ответственных за делопроизводство"}, {"id": "system", "name": "Приказ о вводе информационной системы в эксплуатацию"}}})
}
func checkHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		jsonOut(w, 405, map[string]string{"error": "Используйте POST"})
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 12<<20)
	if err := r.ParseMultipartForm(12 << 20); err != nil {
		jsonOut(w, 400, map[string]string{"error": "Не удалось прочитать загрузку; максимальный размер — 12 МБ"})
		return
	}
	f, h, err := r.FormFile("file")
	if err != nil {
		jsonOut(w, 400, map[string]string{"error": "Передайте файл в поле file"})
		return
	}
	defer f.Close()
	name := filepath.Base(h.Filename)
	var text string
	ext := strings.ToLower(filepath.Ext(name))
	if ext == ".docx" {
		text, err = readDocx(f)
	} else if ext == ".txt" {
		b, _ := io.ReadAll(io.LimitReader(f, 2<<20))
		text = string(b)
	} else {
		jsonOut(w, 415, map[string]string{"error": "Для серверной проверки MVP поддерживаются DOCX и TXT. PDF можно преобразовать в DOCX/TXT."})
		return
	}
	if err != nil {
		jsonOut(w, 400, map[string]string{"error": "Не удалось извлечь текст DOCX"})
		return
	}
	checks := []map[string]string{}
	add := func(ok bool, title, detail string) {
		status := "Нужно проверить"
		if ok {
			status = "Признак найден"
		}
		checks = append(checks, map[string]string{"status": status, "title": title, "detail": detail})
	}
	l := strings.ToLower(text)
	add(strings.Contains(l, "приказываю") || strings.Contains(l, "обязываю"), "Распорядительная часть", "Проверено наличие формулировки «ПРИКАЗЫВАЮ» или «ОБЯЗЫВАЮ». Это автоматическая проверка текста, не юридическое заключение.")
	add(regexp.MustCompile(`(?m)^\s*\d+\.`).MatchString(text), "Нумерация пунктов", "Проверено наличие нумерованных пунктов.")
	add(!strings.Contains(text, "[1]") && !strings.Contains(text, "[2]") && !strings.Contains(text, "__________"), "Незаполненные поля и примечания", "Проверьте отсутствие линий для заполнения, квадратных скобок и номеров примечаний перед подписанием.")
	add(strings.Contains(l, "контроль за исполнением") || strings.Contains(l, "контроль исполнения"), "Контроль исполнения", "Проверено наличие формулировки о контроле исполнения.")
	jsonOut(w, 200, map[string]any{"filename": name, "checks": checks, "note": "Автоматическая предварительная проверка по простым признакам. Она не заменяет сверку с полной Инструкцией, актуальной Матрицей визирования и ответственным подразделением."})
}
func readDocx(r io.Reader) (string, error) {
	b, e := io.ReadAll(io.LimitReader(r, 12<<20))
	if e != nil {
		return "", e
	}
	zr, e := zip.NewReader(strings.NewReader(string(b)), int64(len(b)))
	if e != nil {
		return "", e
	}
	for _, f := range zr.File {
		if f.Name == "word/document.xml" {
			rc, e := f.Open()
			if e != nil {
				return "", e
			}
			defer rc.Close()
			data, e := io.ReadAll(rc)
			if e != nil {
				return "", e
			}
			var doc struct {
				Paras []struct {
					Runs []struct {
						Text string `xml:"t"`
					} `xml:"r"`
				} `xml:"p"`
			}
			if e = xml.Unmarshal(data, &doc); e != nil {
				return "", e
			}
			var out strings.Builder
			for _, p := range doc.Paras {
				for _, run := range p.Runs {
					out.WriteString(run.Text)
				}
				out.WriteByte('\n')
			}
			return out.String(), nil
		}
	}
	return "", fmt.Errorf("document.xml missing")
}
