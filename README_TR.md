# PDF Kütüphanem — sunucu sürümü

Go, PostgreSQL ve LibreOffice kullanır. Word, Excel, PowerPoint ve görselleri PDF yapar. Microsoft Office otomasyonu yerine LibreOffice kullanıldığı için belge düzeninde küçük farklar olabilir.

## Yayına alma

1. Bu klasörü özel bir GitHub deposuna yükleyin. Eski uploads belgelerini yüklemeyin.
2. Docker çalıştırabilen bir sunucuda Dockerfile ile uygulamayı oluşturun.
3. Sunucuda PostgreSQL veritabanı oluşturup bağlantı adresini DATABASE_URL ortam değişkenine girin. Sağlayıcının TLS şartını kullanın. Parolayı kaynak koda yazmayın.
4. PORT sağlayıcı tarafından verilebilir; varsayılan 8080.
5. HTTPS alan adını uygulamaya yönlendirin.
6. Bir DOCX ve JPG yükleyip indirmeyi doğrulayın.

## Yerel geliştirme

LibreOffice kurulu ve libreoffice komutu PATH içinde olmalı. PostgreSQL üzerinde pdf_sitesi veritabanı oluşturun. DATABASE_URL veya DB_PASSWORD ortam değişkenini tanımlayıp go run . çalıştırın.

## Sınırlar ve yayın öncesi kontroller

Bu paket henüz canlıya alınmadı. Gerçek LibreOffice dönüşümü ve Docker derlemesi sunucuda doğrulanmalı. Yüklemeler Office için 25 MiB, görseller için 30 MiB ile sınırlıdır. Office dönüşümü sırayla çalışır ve 90 saniyede sonlandırılır.

Oluşan dosyalar uploads dizininde kalır; otomatik silme ve kullanıcıya özel erişim henüz yoktur. İndirme adresini bilen kişi dosyayı alabilir. Genel kullanıma açmadan önce süreli silme, tahmin edilemez indirme anahtarları, istek sınırı ve kaynak sınırları ekleyin. Dönüştürücüyü hassas sunuculardan ve ağ kaynaklarından izole edin. Kalıcı disk yoksa yeniden başlatmada dosyalar kaybolur. PostgreSQL yalnızca dosya kaydı tutar, dosyanın kendisini saklamaz.

Go sürümü ve bağımlılık sürümleri gönderilen projedeki gibi korundu; sunucuda erişilebilir oldukları doğrulanmalı.
