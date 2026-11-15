package main

import (
	"context"
	"database/sql"
	"fmt"
	"html/template"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/jung-kurt/gofpdf"
)

var db *sql.DB
var conversionSlot = make(chan struct{}, 1)

type ResultData struct {
	Message  string
	FileName string
}

const resultHTML = `<!DOCTYPE html>
<html lang="tr">
<head>
<meta charset="UTF-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>PDF Hazır - PDF Kütüphanem</title>
<meta name="robots" content="noindex">
<style>
*{box-sizing:border-box}
body{
	margin:0;
	background:#f5f6fa;
	color:#292b32;
	font-family:Arial,sans-serif;
}
header{
	background:white;
	padding:25px 8%;
	border-bottom:1px solid #eee;
}
.logo{
	font-size:25px;
	font-weight:bold;
	text-decoration:none;
	color:#292b32;
}
.logo span{color:#e63946}
.card{
	max-width:570px;
	margin:85px auto;
	padding:45px 25px;
	background:white;
	border-radius:18px;
	text-align:center;
	box-shadow:0 12px 35px #00000012;
}
.check{font-size:55px;color:#24a45a}
h1{font-size:28px}
p{color:#777;line-height:1.6}
.btn{
	display:inline-block;
	background:#e63946;
	color:white;
	padding:15px 25px;
	border-radius:9px;
	text-decoration:none;
	font-weight:bold;
	margin:15px 5px;
}
.btn:hover{background:#c82d39}
.back{
	display:inline-block;
	padding:15px;
	color:#555;
	text-decoration:none;
}
</style>
</head>
<body>
<header>
<a class="logo" href="/"><span>PDF</span> Kütüphanem</a>
</header>
<div class="card">
<div class="check">✓</div>
<h1>PDF dosyan hazır!</h1>
<p>{{.Message}}</p>
<a class="btn" href="/pdf/{{.FileName}}">PDF'yi İndir</a>
<a class="back" href="/">Ana Sayfaya Dön</a>
</div>
</body>
</html>`

func main() {
	var err error

	dsn := os.Getenv("DATABASE_URL")

	if dsn == "" {
		password := os.Getenv("DB_PASSWORD")

		if password == "" {
			log.Fatal("DB_PASSWORD veya DATABASE_URL tanımlayın")
		}

		u := &url.URL{
			Scheme:   "postgres",
			User:     url.UserPassword("postgres", password),
			Host:     "localhost:5432",
			Path:     "pdf_sitesi",
			RawQuery: "sslmode=disable",
		}

		dsn = u.String()
	}

	db, err = sql.Open("pgx", dsn)
	if err != nil {
		log.Fatal("Veritabanı hatası: ", err)
	}
	defer db.Close()

	if err = db.Ping(); err != nil {
		log.Fatal("PostgreSQL bağlantı hatası: ", err)
	}

	_, err = db.Exec(`
		CREATE TABLE IF NOT EXISTS pdf_files (
			id SERIAL PRIMARY KEY,
			file_name TEXT NOT NULL,
			file_path TEXT NOT NULL,
			created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
		)
	`)
	if err != nil {
		log.Fatal("Tablo oluşturulamadı: ", err)
	}

	if err = os.MkdirAll("uploads", 0755); err != nil {
		log.Fatal("Klasör hatası: ", err)
	}

	http.HandleFunc("/robots.txt", robotsHandler)
	http.HandleFunc("/", homeHandler)
	http.HandleFunc("/convert", convertOfficeToPDF)
	http.HandleFunc("/convert-jpg", convertImagesToPDF)
	http.HandleFunc("/pdf/", downloadPDF)

	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}

	listener, err := net.Listen("tcp", ":"+port)
	if err != nil {
		log.Fatal("Port hatası: ", err)
	}

	fmt.Println("PostgreSQL bağlantısı başarılı")
	fmt.Println("Sunucu " + port + " portunda çalışıyor")

	server := &http.Server{
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       60 * time.Second,
		WriteTimeout:      150 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	log.Fatal(server.Serve(listener))
}

// Ana sayfanın taranmasına izin verir.
// Dönüştürme ve indirme adreslerini tarama dışında tutar.
func robotsHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "Geçersiz istek", http.StatusMethodNotAllowed)
		return
	}

	w.Header().Set("Content-Type", "text/plain; charset=utf-8")

	if r.Method == http.MethodHead {
		return
	}

	fmt.Fprint(w, "User-agent: *\nDisallow: /pdf/\nDisallow: /convert\n")
}

func homeHandler(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}

	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "Geçersiz istek", http.StatusMethodNotAllowed)
		return
	}

	http.ServeFile(w, r, "index.html")
}

func showResult(w http.ResponseWriter, message, fileName string) {
	t := template.Must(template.New("result").Parse(resultHTML))

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("X-Robots-Tag", "noindex")

	err := t.Execute(w, ResultData{
		Message:  message,
		FileName: fileName,
	})
	if err != nil {
		log.Println("Sonuç sayfası hatası:", err)
	}
}

func generatePDFName() string {
	return fmt.Sprintf("pdf_%d.pdf", time.Now().UnixNano())
}

func savePDFRecord(fileName, filePath string) error {
	_, err := db.Exec(
		"INSERT INTO pdf_files (file_name, file_path) VALUES ($1, $2)",
		fileName,
		filePath,
	)

	return err
}

// Word, Excel ve PowerPoint dosyalarını PDF'ye dönüştürür.
func convertOfficeToPDF(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("X-Robots-Tag", "noindex")

	if r.Method != http.MethodPost {
		http.Error(w, "Geçersiz istek", http.StatusMethodNotAllowed)
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, 25<<20)

	file, header, err := r.FormFile("wordfile")

	if r.MultipartForm != nil {
		defer r.MultipartForm.RemoveAll()
	}

	if err != nil {
		http.Error(
			w,
			"Dosya alınamadı. Bir dosya seç ve toplam boyutun 25 MB sınırını aşmadığından emin ol.",
			http.StatusBadRequest,
		)
		return
	}
	defer file.Close()

	ext := strings.ToLower(filepath.Ext(header.Filename))

	allowed := map[string]bool{
		".doc":  true,
		".docx": true,
		".xls":  true,
		".xlsx": true,
		".ppt":  true,
		".pptx": true,
	}

	if !allowed[ext] {
		http.Error(w, "Desteklenmeyen dosya formatı.", http.StatusBadRequest)
		return
	}

	tempDir, err := os.MkdirTemp("", "pdf-office-*")
	if err != nil {
		http.Error(w, "Geçici klasör oluşturulamadı.", http.StatusInternalServerError)
		return
	}
	defer os.RemoveAll(tempDir)

	tempDir, err = filepath.Abs(tempDir)
	if err != nil {
		http.Error(w, "Dosya yolu oluşturulamadı.", http.StatusInternalServerError)
		return
	}

	inputPath := filepath.Join(tempDir, "document"+ext)
	generatedPDF := filepath.Join(tempDir, "document.pdf")

	outputFile, err := os.Create(inputPath)
	if err != nil {
		http.Error(w, "Dosya kaydedilemedi.", http.StatusInternalServerError)
		return
	}

	_, copyErr := io.Copy(outputFile, file)
	closeErr := outputFile.Close()

	if copyErr != nil || closeErr != nil {
		http.Error(w, "Dosya kopyalanamadı.", http.StatusInternalServerError)
		return
	}

	// Aynı anda tek Office dönüşümü çalışır.
	select {
	case conversionSlot <- struct{}{}:
		defer func() {
			<-conversionSlot
		}()
	default:
		http.Error(
			w,
			"Sunucu meşgul. Biraz sonra tekrar deneyin.",
			http.StatusServiceUnavailable,
		)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 90*time.Second)
	defer cancel()

	profile := (&url.URL{
		Scheme: "file",
		Path:   filepath.ToSlash(filepath.Join(tempDir, "profile")),
	}).String()

	cmd := exec.CommandContext(
		ctx,
		"libreoffice",
		"-env:UserInstallation="+profile,
		"--headless",
		"--nologo",
		"--nodefault",
		"--norestore",
		"--convert-to", "pdf",
		"--outdir", tempDir,
		inputPath,
	)

	output, err := cmd.CombinedOutput()
	if err != nil {
		log.Println("LibreOffice dönüştürme hatası:", string(output), err)

		http.Error(
			w,
			"Belge PDF'ye dönüştürülemedi. Dosyayı kontrol edip tekrar deneyin.",
			http.StatusInternalServerError,
		)
		return
	}

	source, err := os.Open(generatedPDF)
	if err != nil {
		log.Println("PDF bulunamadı:", string(output), err)
		http.Error(w, "Oluşturulan PDF bulunamadı.", http.StatusInternalServerError)
		return
	}
	defer source.Close()

	pdfName := generatePDFName()
	pdfPath := filepath.Join("uploads", pdfName)

	destination, err := os.Create(pdfPath)
	if err != nil {
		http.Error(w, "PDF kaydedilemedi.", http.StatusInternalServerError)
		return
	}

	_, copyErr = io.Copy(destination, source)
	closeErr = destination.Close()

	if copyErr != nil || closeErr != nil {
		os.Remove(pdfPath)
		http.Error(w, "PDF kopyalanamadı.", http.StatusInternalServerError)
		return
	}

	if err := savePDFRecord(pdfName, pdfPath); err != nil {
		os.Remove(pdfPath)
		log.Println("Veritabanı hatası:", err)
		http.Error(w, "PDF veritabanına kaydedilemedi.", http.StatusInternalServerError)
		return
	}

	showResult(w, "Belgen başarıyla PDF'ye dönüştürüldü.", pdfName)
}

// JPG, JPEG ve PNG görsellerini PDF'ye dönüştürür.
func convertImagesToPDF(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("X-Robots-Tag", "noindex")

	if r.Method != http.MethodPost {
		http.Error(w, "Geçersiz istek", http.StatusMethodNotAllowed)
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, 30<<20)

	err := r.ParseMultipartForm(30 << 20)

	if r.MultipartForm != nil {
		defer r.MultipartForm.RemoveAll()
	}

	if err != nil {
		http.Error(w, "Dosyalar çok büyük veya geçersiz.", http.StatusBadRequest)
		return
	}

	files := r.MultipartForm.File["images"]

	if len(files) == 0 {
		http.Error(w, "Lütfen en az bir görsel seç.", http.StatusBadRequest)
		return
	}

	pdf := gofpdf.New("P", "mm", "A4", "")
	pdf.SetMargins(10, 10, 10)
	pdf.SetAutoPageBreak(false, 0)

	for i, header := range files {
		ext := strings.ToLower(filepath.Ext(header.Filename))

		if ext != ".jpg" && ext != ".jpeg" && ext != ".png" {
			http.Error(
				w,
				"Yalnızca JPG, JPEG ve PNG destekleniyor.",
				http.StatusBadRequest,
			)
			return
		}

		file, err := header.Open()
		if err != nil {
			http.Error(w, "Görsel açılamadı.", http.StatusBadRequest)
			return
		}

		imageType := "JPEG"
		if ext == ".png" {
			imageType = "PNG"
		}

		imageName := fmt.Sprintf("image_%d", i)

		info := pdf.RegisterImageOptionsReader(
			imageName,
			gofpdf.ImageOptions{ImageType: imageType},
			file,
		)

		file.Close()

		if info == nil || pdf.Error() != nil {
			http.Error(w, "Görsel PDF'ye eklenemedi.", http.StatusBadRequest)
			return
		}

		width, height := info.Extent()

		if width <= 0 || height <= 0 {
			http.Error(w, "Görsel boyutları geçersiz.", http.StatusBadRequest)
			return
		}

		maxWidth := 190.0
		maxHeight := 277.0

		scale := maxWidth / width
		if height*scale > maxHeight {
			scale = maxHeight / height
		}

		drawWidth := width * scale
		drawHeight := height * scale

		x := (210.0 - drawWidth) / 2
		y := (297.0 - drawHeight) / 2

		pdf.AddPage()

		pdf.ImageOptions(
			imageName,
			x,
			y,
			drawWidth,
			drawHeight,
			false,
			gofpdf.ImageOptions{ImageType: imageType},
			0,
			"",
		)
	}

	pdfName := generatePDFName()
	pdfPath := filepath.Join("uploads", pdfName)

	if err := pdf.OutputFileAndClose(pdfPath); err != nil {
		os.Remove(pdfPath)
		log.Println("PDF hatası:", err)
		http.Error(w, "PDF oluşturulamadı.", http.StatusInternalServerError)
		return
	}

	if err := savePDFRecord(pdfName, pdfPath); err != nil {
		os.Remove(pdfPath)
		log.Println("Veritabanı hatası:", err)
		http.Error(w, "PDF veritabanına kaydedilemedi.", http.StatusInternalServerError)
		return
	}

	showResult(w, "Görsellerin başarıyla PDF'ye dönüştürüldü.", pdfName)
}

// Oluşturulan PDF dosyasını indirir.
func downloadPDF(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("X-Robots-Tag", "noindex")

	if r.Method != http.MethodGet {
		http.Error(w, "Geçersiz istek", http.StatusMethodNotAllowed)
		return
	}

	fileName := strings.TrimPrefix(r.URL.Path, "/pdf/")

	if fileName == "" ||
		fileName != filepath.Base(fileName) ||
		strings.ContainsAny(fileName, "/\\") ||
		!strings.HasSuffix(strings.ToLower(fileName), ".pdf") {
		http.NotFound(w, r)
		return
	}

	pdfPath := filepath.Join("uploads", fileName)

	if _, err := os.Stat(pdfPath); err != nil {
		http.NotFound(w, r)
		return
	}

	w.Header().Set(
		"Content-Disposition",
		fmt.Sprintf(`attachment; filename="%s"`, fileName),
	)

	http.ServeFile(w, r, pdfPath)
}
