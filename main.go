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
	"os"
	"os/exec"
	"path/filepath"
 "net/url"
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
  if password == "" { log.Fatal("DB_PASSWORD veya DATABASE_URL tanımlayın") }
  u := &url.URL{Scheme:"postgres", User:url.UserPassword("postgres", password), Host:"localhost:5432", Path:"pdf_sitesi", RawQuery:"sslmode=disable"}
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

	http.HandleFunc("/", homeHandler)
	http.HandleFunc("/convert", convertOfficeToPDF)
	http.HandleFunc("/convert-jpg", convertImagesToPDF)
	http.HandleFunc("/pdf/", downloadPDF)

	port := os.Getenv("PORT")
 if port == "" { port = "8080" }
	listener, err := net.Listen("tcp", ":"+port)

 if err != nil { log.Fatal("Port hatası: ", err) }

	fmt.Println("PostgreSQL bağlantısı başarılı")
	fmt.Println("Site http://localhost:" + port + " adresinde açılıyor")

	server := &http.Server{ReadHeaderTimeout: 10*time.Second, ReadTimeout: 60*time.Second, WriteTimeout: 150*time.Second, IdleTimeout: 60*time.Second}
 log.Fatal(server.Serve(listener))
}

func homeHandler(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}

	if r.Method != http.MethodGet {
		http.Error(w, "Geçersiz istek", http.StatusMethodNotAllowed)
		return
	}

	http.ServeFile(w, r, "index.html")
}

func showResult(w http.ResponseWriter, message, fileName string) {
	t := template.Must(template.New("result").Parse(resultHTML))
	w.Header().Set("Content-Type", "text/html; charset=utf-8")

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

// Microsoft Office dosyalarını PDF'ye dönüştürür.
func convertOfficeToPDF(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Geçersiz istek", http.StatusMethodNotAllowed)
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, 25<<20)

	file, header, err := r.FormFile("wordfile")
	if err != nil {
		http.Error(w, "Lütfen bir dosya seç.", http.StatusBadRequest)
		return
	}
	defer file.Close()
 if r.MultipartForm != nil { defer r.MultipartForm.RemoveAll() }

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
		http.Error(w, "Geçici klasör oluşturulamadı.", 500)
		return
	}
	defer os.RemoveAll(tempDir)

	inputPath := filepath.Join(tempDir, "document"+ext)

	outputFile, err := os.Create(inputPath)
	if err != nil {
		http.Error(w, "Dosya kaydedilemedi.", 500)
		return
	}

	_, copyErr := io.Copy(outputFile, file)
	closeErr := outputFile.Close()

	if copyErr != nil || closeErr != nil {
		http.Error(w, "Dosya kopyalanamadı.", 500)
		return
	}

	// Dosya yollarını mutlak hale getir.
	inputPath, err = filepath.Abs(inputPath)
	if err != nil {
		http.Error(w, "Dosya yolu oluşturulamadı.", 500)
		return
	}

	generatedPDF := filepath.Join(tempDir, "document.pdf")
	generatedPDF, err = filepath.Abs(generatedPDF)
	if err != nil {
		http.Error(w, "PDF yolu oluşturulamadı.", 500)
		return
	}

 // Her dönüşüm ayrı profil kullanır; aynı anda tek Office işi çalışır.
 select {
 case conversionSlot <- struct{}{}:
  defer func(){ <-conversionSlot }()
 default:
  http.Error(w, "Sunucu meşgul. Biraz sonra tekrar deneyin.", http.StatusServiceUnavailable)
  return
 }
 ctx, cancel := context.WithTimeout(r.Context(), 90*time.Second)
 defer cancel()
 profile := (&url.URL{Scheme:"file", Path:filepath.ToSlash(filepath.Join(tempDir,"profile"))}).String()
 cmd := exec.CommandContext(ctx, "libreoffice", "-env:UserInstallation="+profile,
  "--headless", "--nologo", "--nodefault", "--norestore",
  "--convert-to", "pdf", "--outdir", tempDir, inputPath)

	output, err := cmd.CombinedOutput()

	if err != nil {
		log.Println("LibreOffice dönüştürme hatası:", string(output), err)
		http.Error(
			w,
			"Belge PDF'ye dönüştürülemedi. Terminaldeki hatayı kontrol edin.",
			500,
		)
		return
	}

	source, err := os.Open(generatedPDF)
	if err != nil {
		log.Println("PDF bulunamadı:", string(output), err)
		http.Error(w, "Oluşturulan PDF bulunamadı.", 500)
		return
	}
	defer source.Close()

	pdfName := generatePDFName()
	pdfPath := filepath.Join("uploads", pdfName)

	destination, err := os.Create(pdfPath)
	if err != nil {
		http.Error(w, "PDF kaydedilemedi.", 500)
		return
	}

	_, copyErr = io.Copy(destination, source)
	closeErr = destination.Close()

	if copyErr != nil || closeErr != nil {
		os.Remove(pdfPath)
		http.Error(w, "PDF kopyalanamadı.", 500)
		return
	}

	if err := savePDFRecord(pdfName, pdfPath); err != nil {
		os.Remove(pdfPath)
		log.Println("Veritabanı hatası:", err)
		http.Error(w, "PDF veritabanına kaydedilemedi.", 500)
		return
	}

	showResult(w, "Belgen başarıyla PDF'ye dönüştürüldü.", pdfName)
}

// JPG, JPEG ve PNG görsellerini PDF'ye dönüştürür.
func convertImagesToPDF(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Geçersiz istek", http.StatusMethodNotAllowed)
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, 30<<20)

	if err := r.ParseMultipartForm(30 << 20); err != nil {
		http.Error(w, "Dosyalar çok büyük veya geçersiz.", 400)
		return
	}
	defer r.MultipartForm.RemoveAll()

	files := r.MultipartForm.File["images"]

	if len(files) == 0 {
		http.Error(w, "Lütfen en az bir görsel seç.", 400)
		return
	}

	pdf := gofpdf.New("P", "mm", "A4", "")
	pdf.SetMargins(10, 10, 10)
	pdf.SetAutoPageBreak(false, 0)

	for i, header := range files {
		ext := strings.ToLower(filepath.Ext(header.Filename))

		if ext != ".jpg" && ext != ".jpeg" && ext != ".png" {
			http.Error(w, "Yalnızca JPG, JPEG ve PNG destekleniyor.", 400)
			return
		}

		file, err := header.Open()
		if err != nil {
			http.Error(w, "Görsel açılamadı.", 400)
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
			http.Error(w, "Görsel PDF'ye eklenemedi.", 400)
			return
		}

		width, height := info.Extent()

		if width <= 0 || height <= 0 {
			http.Error(w, "Görsel boyutları geçersiz.", 400)
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
		log.Println("PDF hatası:", err)
		http.Error(w, "PDF oluşturulamadı.", 500)
		return
	}

	if err := savePDFRecord(pdfName, pdfPath); err != nil {
		os.Remove(pdfPath)
		log.Println("Veritabanı hatası:", err)
		http.Error(w, "PDF veritabanına kaydedilemedi.", 500)
		return
	}

	showResult(w, "Görsellerin başarıyla PDF'ye dönüştürüldü.", pdfName)
}

// Oluşturulan PDF dosyasını indirir.
func downloadPDF(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Geçersiz istek", http.StatusMethodNotAllowed)
		return
	}

	fileName := strings.TrimPrefix(r.URL.Path, "/pdf/")

	if fileName == "" ||
		fileName != filepath.Base(fileName) ||
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
