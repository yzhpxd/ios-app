package main

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"fmt"
	"html/template"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"howett.net/plist"
)

const (
	uploadDir = "./uploads"
	metaFile  = "./uploads/meta.json"
)

type AppRecord struct {
	FileName   string    `json:"file_name"`
	Name       string    `json:"name"`
	BundleID   string    `json:"bundle_id"`
	Version    string    `json:"version"`
	FileSize   string    `json:"file_size"`
	UploadTime time.Time `json:"upload_time"`
}

type InfoPlist struct {
	CFBundleDisplayName string `plist:"CFBundleDisplayName"`
	CFBundleName        string `plist:"CFBundleName"`
	CFBundleIdentifier  string `plist:"CFBundleIdentifier"`
	CFBundleShortVer    string `plist:"CFBundleShortVersionString"`
}

func main() {
	_ = os.MkdirAll(uploadDir, 0755)

	http.HandleFunc("/", handleIndex)
	http.HandleFunc("/upload", handleUpload)
	http.HandleFunc("/install/", handleInstallPage)
	http.HandleFunc("/plist/", handlePlist)
	http.HandleFunc("/delete", handleDelete)
	http.Handle("/uploads/", http.StripPrefix("/uploads/", http.FileServer(http.Dir(uploadDir))))

	port := os.Getenv("PORT")
	if port == "" {
		port = "8061"
	}

	fmt.Printf("IPA 服务已启动，监听端口: :%s\n", port)
	if err := http.ListenAndServe(":"+port, nil); err != nil {
		log.Fatalf("启动失败: %v", err)
	}
}

// 统一获取对外可信的 BaseURL（强制锁定 https，适配 Caddy 反代）
func getBaseURL(r *http.Request) string {
	scheme := "https"
	host := r.Host
	if fh := r.Header.Get("X-Forwarded-Host"); fh != "" {
		host = fh
	}
	return fmt.Sprintf("%s://%s", scheme, host)
}

func loadRecords() []AppRecord {
	data, err := os.ReadFile(metaFile)
	if err != nil {
		return []AppRecord{}
	}
	var records []AppRecord
	_ = json.Unmarshal(data, &records)
	return records
}

func saveRecords(records []AppRecord) {
	data, _ := json.MarshalIndent(records, "", "  ")
	_ = os.WriteFile(metaFile, data, 0644)
}

func handleIndex(w http.ResponseWriter, r *http.Request) {
	records := loadRecords()
	sort.Slice(records, func(i, j int) bool {
		return records[i].UploadTime.After(records[j].UploadTime)
	})

	tmpl := `<!DOCTYPE html>
<html lang="zh-CN">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1.0, maximum-scale=1.0">
  <title>IPA 在线分发站</title>
  <style>
    :root { --primary: #0071e3; --bg: #f5f5f7; --card: #ffffff; }
    body { font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, sans-serif; background: var(--bg); margin: 0; padding: 20px; color: #1d1d1f; }
    .container { max-width: 860px; margin: 0 auto; }
    h1 { font-size: 26px; font-weight: 600; margin-bottom: 20px; text-align: center; }
    
    .dropzone {
      background: var(--card); border: 2px dashed #c7c7cc; border-radius: 16px;
      padding: 40px 20px; text-align: center; cursor: pointer; transition: 0.2s; position: relative;
    }
    .dropzone.dragover { border-color: var(--primary); background: #e8f2ff; }
    .dropzone p { margin: 8px 0; color: #86868b; }
    .dropzone .icon { font-size: 48px; }
    #fileInput { display: none; }

    .progress-box { display: none; margin-top: 15px; }
    .progress-bar { width: 100%; height: 8px; background: #e5e5ea; border-radius: 4px; overflow: hidden; }
    .progress-fill { height: 100%; width: 0%; background: var(--primary); transition: width 0.1s; }

    .section-title { font-size: 18px; margin: 35px 0 15px; font-weight: 600; }
    .app-list { display: flex; flex-direction: column; gap: 12px; }
    .app-item {
      background: var(--card); border-radius: 12px; padding: 16px 20px; display: flex;
      justify-content: space-between; align-items: center; box-shadow: 0 2px 8px rgba(0,0,0,0.04);
    }
    .app-info { flex: 1; }
    .app-title { font-size: 16px; font-weight: 600; margin-bottom: 4px; }
    .app-meta { font-size: 13px; color: #86868b; word-break: break-all; }
    .app-actions { display: flex; gap: 10px; align-items: center; }
    .btn {
      padding: 8px 16px; border-radius: 8px; text-decoration: none; font-size: 14px;
      font-weight: 500; cursor: pointer; border: none; display: inline-block;
    }
    .btn-install { background: #34c759; color: #fff; }
    .btn-del { background: #ff3b30; color: #fff; }
    .empty-tip { text-align: center; color: #86868b; padding: 30px; background: var(--card); border-radius: 12px; }
  </style>
</head>
<body>
  <div class="container">
    <h1>📦 iOS IPA 专属分发站</h1>

    <div class="dropzone" id="dropZone" onclick="document.getElementById('fileInput').click()">
      <div class="icon">☁️</div>
      <h3 style="margin: 8px 0;">点击上传 或 将 IPA 文件直接拖入此处</h3>
      <p>支持大文件上传，自动解析 Bundle ID 与版本信息</p>
      <input type="file" id="fileInput" accept=".ipa" onchange="uploadFile(this.files[0])">
      <div class="progress-box" id="progressBox">
        <div class="progress-bar"><div class="progress-fill" id="progressFill"></div></div>
        <p id="progressText" style="margin-top: 6px; font-size: 12px;">上传中 0%...</p>
      </div>
    </div>

    <div class="section-title">已上传应用列表 ({{len .Records}})</div>
    <div class="app-list">
      {{if not .Records}}
        <div class="empty-tip">暂无历史上传，拖拽 IPA 文件即可生成分发链接</div>
      {{end}}
      {{range .Records}}
      <div class="app-item">
        <div class="app-info">
          <div class="app-title">{{.Name}} <span style="font-size:12px; font-weight:normal; background:#efeff4; padding:2px 8px; border-radius:4px;">v{{.Version}}</span></div>
          <div class="app-meta">包名: {{.BundleID}} | 大小: {{.FileSize}} | 时间: {{.UploadTime.Format "2006-01-02 15:04"}}</div>
        </div>
        <div class="app-actions">
          <a class="btn btn-install" href="/install/{{.FileName}}">安装页</a>
          <button class="btn btn-del" onclick="deleteApp('{{.FileName}}')">删除</button>
        </div>
      </div>
      {{end}}
    </div>
  </div>

  <script>
    const dropZone = document.getElementById('dropZone');
    const fileInput = document.getElementById('fileInput');

    ['dragenter', 'dragover'].forEach(name => {
      dropZone.addEventListener(name, (e) => { e.preventDefault(); dropZone.classList.add('dragover'); });
    });
    ['dragleave', 'drop'].forEach(name => {
      dropZone.addEventListener(name, (e) => { e.preventDefault(); dropZone.classList.remove('dragover'); });
    });

    dropZone.addEventListener('drop', (e) => {
      if (e.dataTransfer.files.length > 0) {
        uploadFile(e.dataTransfer.files[0]);
      }
    });

    function uploadFile(file) {
      if (!file || !file.name.endsWith('.ipa')) {
        alert('请选择以 .ipa 结尾的 iOS 安装包！');
        return;
      }

      const fd = new FormData();
      fd.append('ipa', file);

      const xhr = new XMLHttpRequest();
      const progressBox = document.getElementById('progressBox');
      const progressFill = document.getElementById('progressFill');
      const progressText = document.getElementById('progressText');

      progressBox.style.display = 'block';

      xhr.upload.onprogress = (e) => {
        if (e.lengthComputable) {
          const percent = Math.round((e.loaded / e.total) * 100);
          progressFill.style.width = percent + '%';
          progressText.innerText = '正在上传并解析: ' + percent + '%';
        }
      };

      xhr.onload = () => {
        if (xhr.status === 200) {
          const res = JSON.parse(xhr.responseText);
          window.location.href = res.redirect;
        } else {
          alert('上传解析失败: ' + xhr.responseText);
          progressBox.style.display = 'none';
        }
      };

      xhr.open('POST', '/upload', true);
      xhr.send(fd);
    }

    function deleteApp(filename) {
      if (confirm('确定删除该安装包吗？')) {
        fetch('/delete?file=' + encodeURIComponent(filename), { method: 'POST' })
          .then(r => r.text())
          .then(() => location.reload());
      }
    }
  </script>
</body>
</html>`

	t, _ := template.New("index").Parse(tmpl)
	_ = t.Execute(w, map[string]interface{}{"Records": records})
}

func handleUpload(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", 405)
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, 2<<30)
	if err := r.ParseMultipartForm(64 << 20); err != nil {
		http.Error(w, "文件过大或解析失败", 400)
		return
	}

	file, header, err := r.FormFile("ipa")
	if err != nil {
		http.Error(w, "无法读取文件: "+err.Error(), 400)
		return
	}
	defer file.Close()

	destPath := filepath.Join(uploadDir, header.Filename)
	out, err := os.Create(destPath)
	if err != nil {
		http.Error(w, "写入磁盘失败", 500)
		return
	}
	defer out.Close()

	n, _ := io.Copy(out, file)

	info, err := parseIPA(destPath)
	if err != nil {
		info = AppInfo{Name: strings.TrimSuffix(header.Filename, ".ipa"), BundleID: "com.custom.app", Version: "1.0"}
	}
	info.FileName = header.Filename

	record := AppRecord{
		FileName:   header.Filename,
		Name:       info.Name,
		BundleID:   info.BundleID,
		Version:    info.Version,
		FileSize:   fmt.Sprintf("%.1f MB", float64(n)/(1024*1024)),
		UploadTime: time.Now(),
	}

	records := loadRecords()
	updated := false
	for i, rItem := range records {
		if rItem.FileName == record.FileName {
			records[i] = record
			updated = true
			break
		}
	}
	if !updated {
		records = append(records, record)
	}
	saveRecords(records)

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{
		"redirect": "/install/" + url.PathEscape(header.Filename),
	})
}

func handleInstallPage(w http.ResponseWriter, r *http.Request) {
	rawFile := strings.TrimPrefix(r.URL.Path, "/install/")
	fileName, _ := url.PathUnescape(rawFile)

	records := loadRecords()
	var current AppRecord
	for _, rec := range records {
		if rec.FileName == fileName {
			current = rec
			break
		}
	}

	if current.FileName == "" {
		current = AppRecord{FileName: fileName, Name: fileName, BundleID: "com.unknown.app", Version: "1.0"}
	}

	baseURL := getBaseURL(r)
	plistDownloadURL := fmt.Sprintf("%s/plist/%s", baseURL, url.PathEscape(fileName))
	itmsURL := fmt.Sprintf("itms-services://?action=download-manifest&url=%s", url.QueryEscape(plistDownloadURL))

	tmpl := `<!DOCTYPE html>
<html lang="zh-CN">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1.0, maximum-scale=1.0">
  <title>{{.Record.Name}} - 在线安装</title>
  <style>
    body { font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, sans-serif; background: #f5f5f7; margin: 0; padding: 30px 15px; display: flex; justify-content: center; }
    .card { background: #fff; border-radius: 16px; padding: 35px 25px; max-width: 420px; width: 100%; box-shadow: 0 4px 20px rgba(0,0,0,0.06); text-align: center; }
    .icon { width: 72px; height: 72px; background: #007aff; border-radius: 16px; margin: 0 auto 15px; display: flex; align-items: center; justify-content: center; font-size: 34px; color: #fff; }
    h2 { margin: 0 0 8px; font-size: 22px; }
    .meta { font-size: 14px; color: #86868b; line-height: 1.6; margin-bottom: 25px; }
    .btn { display: block; width: 100%; box-sizing: border-box; background: #34c759; color: #fff; text-decoration: none; padding: 15px 0; border-radius: 12px; font-size: 18px; font-weight: 600; }
    .btn:active { opacity: 0.85; }
    .qr-box { margin-top: 25px; border-top: 1px solid #f0f0f2; padding-top: 25px; }
    #qrcode { display: flex; justify-content: center; margin-bottom: 8px; }
    .back { margin-top: 20px; display: inline-block; color: #0071e3; text-decoration: none; font-size: 14px; }
  </style>
</head>
<body>
  <div class="card">
    <div class="icon">📱</div>
    <h2>{{.Record.Name}}</h2>
    <div class="meta">
      <div>版本：{{.Record.Version}} ({{.Record.FileSize}})</div>
      <div style="word-break:break-all; font-size:12px; margin-top:4px;">{{.Record.BundleID}}</div>
    </div>
    
    <a class="btn" href="{{.ItmsURL}}" onclick="handleInstallClick()">点击在线安装</a>

    <div class="qr-box">
      <div id="qrcode"></div>
      <p style="font-size: 13px; color: #86868b; margin: 8px 0 0;">iPhone 自带相机或 Safari 扫码安装</p>
    </div>

    <div><a class="back" href="/">← 返回应用列表</a></div>
  </div>

  <script src="https://cdn.jsdelivr.net/npm/qrcodejs@1.0.0/qrcode.min.js"></script>
  <script>
    new QRCode(document.getElementById("qrcode"), {
      text: window.location.href,
      width: 170,
      height: 170,
      correctLevel: QRCode.CorrectLevel.M
    });

    function handleInstallClick() {
      var ua = navigator.userAgent.toLowerCase();
      var isSafari = /safari/.test(ua) && !/chrome|crios|fxios/.test(ua);
      if (!isSafari) {
        alert('温馨提示：iOS 在线安装必须使用 iPhone 自带的【Safari 浏览器】打开该页面！');
      }
    }
  </script>
</body>
</html>`

	t, _ := template.New("install").Parse(tmpl)
	_ = t.Execute(w, map[string]interface{}{
		"Record":  current,
		"ItmsURL": template.URL(itmsURL),
	})
}

func handlePlist(w http.ResponseWriter, r *http.Request) {
	rawFile := strings.TrimPrefix(r.URL.Path, "/plist/")
	fileName, _ := url.PathUnescape(rawFile)

	records := loadRecords()
	var current AppRecord
	for _, rec := range records {
		if rec.FileName == fileName {
			current = rec
			break
		}
	}

	if current.FileName == "" {
		current = AppRecord{FileName: fileName, Name: fileName, BundleID: "com.custom.app", Version: "1.0"}
	}

	baseURL := getBaseURL(r)
	ipaURL := fmt.Sprintf("%s/uploads/%s", baseURL, url.PathEscape(fileName))

	// 核心：强制伪装版本号为 999.0，欺骗 iOS 判定为“升级”，从而突破降级限制实现无条件覆盖安装
	forceVersion := "999.0"

	plistContent := fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>items</key>
  <array>
    <dict>
      <key>assets</key>
      <array>
        <dict>
          <key>kind</key>
          <string>software-package</string>
          <key>url</key>
          <string>%s</string>
        </dict>
      </array>
      <key>metadata</key>
      <dict>
        <key>bundle-identifier</key>
        <string>%s</string>
        <key>bundle-version</key>
        <string>%s</string>
        <key>kind</key>
        <string>software</string>
        <key>title</key>
        <string>%s</string>
      </dict>
    </dict>
  </array>
</dict>
</plist>`, ipaURL, current.BundleID, forceVersion, current.Name)

	w.Header().Set("Content-Type", "application/xml; charset=utf-8")
	_, _ = w.Write([]byte(plistContent))
}

func handleDelete(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", 405)
		return
	}
	target := r.URL.Query().Get("file")
	_ = os.Remove(filepath.Join(uploadDir, target))

	records := loadRecords()
	var filtered []AppRecord
	for _, rec := range records {
		if rec.FileName != target {
			filtered = append(filtered, rec)
		}
	}
	saveRecords(filtered)
	w.WriteHeader(http.StatusOK)
}

type AppInfo struct {
	Name     string
	BundleID string
	Version  string
	FileName string
}

func parseIPA(path string) (AppInfo, error) {
	zr, err := zip.OpenReader(path)
	if err != nil {
		return AppInfo{}, err
	}
	defer zr.Close()

	for _, f := range zr.File {
		if strings.HasPrefix(f.Name, "Payload/") && strings.HasSuffix(f.Name, ".app/Info.plist") {
			rc, err := f.Open()
			if err != nil {
				return AppInfo{}, err
			}
			buf := new(bytes.Buffer)
			_, _ = io.Copy(buf, rc)
			rc.Close()

			var p InfoPlist
			decoder := plist.NewDecoder(bytes.NewReader(buf.Bytes()))
			if err := decoder.Decode(&p); err == nil {
				name := p.CFBundleDisplayName
				if name == "" {
					name = p.CFBundleName
				}
				if name == "" {
					name = filepath.Base(path)
				}
				return AppInfo{
					Name:     name,
					BundleID: p.CFBundleIdentifier,
					Version:  p.CFBundleShortVer,
				}, nil
			}
		}
	}
	return AppInfo{}, fmt.Errorf("plist not found")
}
