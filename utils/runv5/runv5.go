package runv5

import (
	"context"
	"encoding/base64"
	"fmt"
	"path/filepath"
	"time"
	"os"
	"bytes"
	"errors"
	"io"
	"encoding/json"
	"net/http"
	"net/url"
	"os/exec"
	"strings"
	"sync"

	cdm "main/utils/runv3/cdm"
	key "main/utils/runv3/key"
	"main/utils/httputil"

	"github.com/go-resty/resty/v2"
	"google.golang.org/protobuf/proto"
	"github.com/itouakirai/mp4ff/mp4"
	"github.com/grafov/m3u8"
	"github.com/schollz/progressbar/v3"
)

// PlaybackLicense 是 wrapper-lite /license 接口的响应结构。
// runv3 中对应的是 Apple acquireWebPlaybackLicense 的响应结构，
// 这里改为解析 lite-server 的 {code,msg,data:{license,...}} 信封。
type PlaybackLicense struct {
	Code int    `json:"code"`
	Msg  string `json:"msg"`
	Data struct {
		AdamId  string `json:"adamId"`
		License string `json:"license"`
		Renew   int    `json:"renew"`
	} `json:"data"`
}

const widevineKeyFormat = "urn:uuid:edef8ba9-79d6-4ace-a3c8-27dcd51d21ed"

func getPSSH(contentId string, kidBase64 string) (string, error) {
	kidBytes, err := base64.StdEncoding.DecodeString(kidBase64)
	if err != nil {
		return "", fmt.Errorf("failed to decode base64 KID: %v", err)
	}
	contentIdEncoded := base64.StdEncoding.EncodeToString([]byte(contentId))
	algo := cdm.WidevineCencHeader_AESCTR
	widevineCencHeader := &cdm.WidevineCencHeader{
		KeyId:     [][]byte{kidBytes},
		Algorithm: &algo,
		Provider:  new(string),
		ContentId: []byte(contentIdEncoded),
		Policy:    new(string),
	}
	widevineCenc, err := proto.Marshal(widevineCencHeader)
	if err != nil {
		return "", fmt.Errorf("failed to marshal WidevineCencHeader: %v", err)
	}
	//最前面添加32字节
	widevineCenc = append([]byte("0123456789abcdef0123456789abcdef"), widevineCenc...)
	pssh := base64.StdEncoding.EncodeToString(widevineCenc)
	return pssh, nil
}

// BeforeRequest 与 runv3 相同，只是把 license 请求发到 wrapper-lite 的
// /license 接口（url 由 Run 传入，即 lite-server + "/license"），
// 不再需要 Authorization / media-user-token。
func BeforeRequest(cl *resty.Client, ctx context.Context, url string, body []byte) (*resty.Response, error) {
	uri := ctx.Value("uriPrefix").(string) + "," + ctx.Value("pssh").(string)
	jsondata := map[string]interface{}{
		// wrapper-lite /license 只接收这三个字段，多余字段（key-system /
		// isLibrary / user-initiated）由 lite-server 自己补上再转发 Apple，
		// 客户端不需要（也不应该）发送。
		"challenge": base64.StdEncoding.EncodeToString(body),
		"uri":       uri,
		"adamId":    ctx.Value("adamId").(string),
	}

	resp, err := cl.R().
		SetContext(ctx).
		SetBody(jsondata).
		Post(url)

	if err != nil {
		fmt.Println(err)
	}

	return resp, err
}

// AfterRequest 解析 wrapper-lite /license 的信封并返回 license 原始字节。
func AfterRequest(response *resty.Response) ([]byte, error) {
	var responseData PlaybackLicense

	err := json.Unmarshal(response.Body(), &responseData)
	if err != nil {
		return nil, fmt.Errorf("failed to parse response JSON: %v", err)
	}

	if responseData.Code != 0 {
		return nil, fmt.Errorf("lite-server /license returned code=%d msg=%s", responseData.Code, responseData.Msg)
	}
	if responseData.Data.License == "" {
		return nil, errors.New("empty license in lite-server response")
	}

	license, err := base64.StdEncoding.DecodeString(responseData.Data.License)
	if err != nil {
		return nil, fmt.Errorf("failed to decode license: %v", err)
	}

	return license, nil
}

func getPlaybackHeaders(authtoken string, mutoken string) map[string]string {
	headers := map[string]string{
		"User-Agent":          "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/91.0.4472.124 Safari/537.36",
		"Origin":              "https://music.apple.com",
		"Referer":             "https://music.apple.com/",
		"Accept":              "application/vnd.apple.mpegurl,application/x-mpegURL,text/plain;q=0.8,*/*;q=0.5",
		"X-Apple-Store-Front": "143441-1,25",
	}
	if mutoken != "" {
		headers["x-apple-music-user-token"] = mutoken
		headers["Media-User-Token"] = mutoken
	}
	return headers
}

func getURLWithHeaders(url string, authtoken string, mutoken string) ([]byte, error) {
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, err
	}
	for key, value := range getPlaybackHeaders(authtoken, mutoken) {
		req.Header.Set(key, value)
	}
	resp, err := httputil.Client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("request failed with status %s", resp.Status)
	}
	return io.ReadAll(resp.Body)
}

// GetWebplayback 与 runv3 同名同返回值，但不再请求 Apple webPlayback，
// 改为请求 wrapper-lite 的 /webplayback 接口，因此不需要 media-user-token。
// liteServer 为 lite-server 地址，如 "http://127.0.0.1:8080"。
func GetWebplayback(adamId string, liteServer string, mvmode bool) (string, string, string, error) {
	if liteServer == "" {
		return "", "", "", errors.New("lite-server is not configured")
	}
	endpoint := strings.TrimRight(liteServer, "/") + "/webplayback?adamId=" + url.QueryEscape(adamId)
	req, err := http.NewRequest("GET", endpoint, nil)
	if err != nil {
		return "", "", "", err
	}
	resp, err := httputil.Client.Do(req)
	if err != nil {
		return "", "", "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", "", "", fmt.Errorf("lite-server /webplayback returned %s", resp.Status)
	}
	var envelope struct {
		Code int    `json:"code"`
		Msg  string `json:"msg"`
		Data struct {
			M3u8 string `json:"m3u8"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&envelope); err != nil {
		return "", "", "", err
	}
	if envelope.Code != 0 {
		return "", "", "", fmt.Errorf("lite-server /webplayback returned code=%d msg=%s", envelope.Code, envelope.Msg)
	}
	if envelope.Data.M3u8 == "" {
		return "", "", "", errors.New("Unavailable")
	}
	if mvmode {
		return envelope.Data.M3u8, "", "", nil
	}
	kidBase64, fileurl, uriPrefix, err := extractKidBase64(envelope.Data.M3u8, false)
	if err != nil {
		return "", "", "", err
	}
	return fileurl, kidBase64, uriPrefix, nil
}

func ResolveStationVariantPlaylist(masterURL string, authtoken string, mutoken string) (string, error) {
	body, err := getURLWithHeaders(masterURL, authtoken, mutoken)
	if err != nil {
		return "", err
	}
	masterString := string(body)
	from, listType, err := m3u8.DecodeFrom(strings.NewReader(masterString), true)
	if err != nil {
		return "", err
	}
	if listType != m3u8.MASTER {
		return masterURL, nil
	}
	masterPlaylist := from.(*m3u8.MasterPlaylist)
	var preferred string
	for _, variant := range masterPlaylist.Variants {
		if variant == nil {
			continue
		}
		uri := variant.URI
		if strings.Contains(uri, "256") || strings.Contains(uri, "256_6") {
			preferred = uri
			break
		}
		if preferred == "" {
			preferred = uri
		}
	}
	if preferred == "" {
		return masterURL, nil
	}
	if strings.HasPrefix(preferred, "http") {
		return preferred, nil
	}
	lastSlashIndex := strings.LastIndex(masterURL, "/")
	if lastSlashIndex == -1 {
		return masterURL, nil
	}
	return masterURL[:lastSlashIndex+1] + preferred, nil
}

func extractKidBase64(b string, mvmode bool) (string, string, string, error) {
	resp, err := http.Get(b)
	if err != nil {
		return "", "", "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", "", "", errors.New(resp.Status)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", "", "", err
	}
	masterString := string(body)
	from, listType, err := m3u8.DecodeFrom(strings.NewReader(masterString), true)
	if err != nil {
		return "", "", "", err
	}
	var kidbase64 string
	var uriPrefix string
	var urlBuilder strings.Builder
	if listType == m3u8.MEDIA {
		mediaPlaylist := from.(*m3u8.MediaPlaylist)
		if mediaPlaylist.Key != nil {
			split := strings.Split(mediaPlaylist.Key.URI, ",")
			uriPrefix = split[0]
			kidbase64 = split[1]
			lastSlashIndex := strings.LastIndex(b, "/")
			// 截取最后一个斜杠之前的部分
			urlBuilder.WriteString(b[:lastSlashIndex])
			urlBuilder.WriteString("/")
			urlBuilder.WriteString(mediaPlaylist.Map.URI)
			//fileurl = b[:lastSlashIndex] + "/" + mediaPlaylist.Map.URI
			//fmt.Println("Extracted URI:", mediaPlaylist.Map.URI)
			if mvmode {
				for _, segment := range mediaPlaylist.Segments {
					if segment != nil {
						//fmt.Println("Extracted URI:", segment.URI)
						urlBuilder.WriteString(";")
						urlBuilder.WriteString(b[:lastSlashIndex])
						urlBuilder.WriteString("/")
						urlBuilder.WriteString(segment.URI)
						//fileurl = fileurl + ";" + b[:lastSlashIndex] + "/" + segment.URI
					}
				}
			}
		} else {
			fmt.Println("No key information found")
		}
	} else {
		fmt.Println("Not a media playlist")
	}
	return kidbase64, urlBuilder.String(), uriPrefix, nil
}

func extsong(b string) bytes.Buffer {
	resp, err := http.Get(b)
	if err != nil {
		fmt.Printf("下载文件失败: %v\n", err)
	}
	defer resp.Body.Close()
	var buffer bytes.Buffer
	bar := progressbar.NewOptions64(
		resp.ContentLength,
		progressbar.OptionClearOnFinish(),
		progressbar.OptionSetElapsedTime(false),
		progressbar.OptionSetPredictTime(false),
		progressbar.OptionShowElapsedTimeOnFinish(),
		progressbar.OptionShowCount(),
		progressbar.OptionEnableColorCodes(true),
		progressbar.OptionShowBytes(true),
		progressbar.OptionSetDescription("Downloading..."),
		progressbar.OptionSetTheme(progressbar.Theme{
			Saucer:        "",
			SaucerHead:    "",
			SaucerPadding: "",
			BarStart:      "",
			BarEnd:        "",
		}),
	)
	io.Copy(io.MultiWriter(&buffer, bar), resp.Body)
	return buffer
}

// Run 与 runv3.Run 签名一致，调用方可以无缝切换。authtoken / mutoken 不再使用，
// liteServerUrl 为 wrapper-lite 地址（如 "http://127.0.0.1:8080"），
// license 会发到 liteServerUrl + "/license"，webplayback 走 liteServerUrl + "/webplayback"。
func Run(adamId string, trackpath string, authtoken string, mvmode bool, liteServerUrl string) (string, error) {
	if liteServerUrl == "" {
		return "", errors.New("lite-server is not configured")
	}
	var keystr string //for mv key
	var fileurl string
	var kidBase64 string
	var uriPrefix string
	var err error
	if mvmode {
		kidBase64, fileurl, uriPrefix, err = extractKidBase64(trackpath, true)
		if err != nil {
			return "", err
		}
	} else {
		fileurl, kidBase64, uriPrefix, err = GetWebplayback(adamId, liteServerUrl, false)
		if err != nil {
			return "", err
		}
	}
	ctx := context.Background()
	ctx = context.WithValue(ctx, "pssh", kidBase64)
	ctx = context.WithValue(ctx, "adamId", adamId)
	ctx = context.WithValue(ctx, "uriPrefix", uriPrefix)
	pssh, err := getPSSH("", kidBase64)
	//fmt.Println(pssh)
	if err != nil {
		fmt.Println(err)
		return "", err
	}
	client := resty.New()
	key := key.Key{
		ReqCli:        client,
		BeforeRequest: BeforeRequest,
		AfterRequest:  AfterRequest,
	}
	key.CdmInit()
	keystr, keybt, err := key.GetKey(ctx, liteServerUrl+"/license", pssh, nil)
	if err != nil {
		fmt.Println(err)
		return "", err
	}
	if mvmode {
		keyAndUrls := "1:" + keystr + ";" + fileurl
		return keyAndUrls, nil
	}
	body := extsong(fileurl)
	fmt.Print("Downloaded\n")
	//bodyReader := bytes.NewReader(body)
	var buffer bytes.Buffer

	err = DecryptMP4(&body, keybt, &buffer)
	if err != nil {
		fmt.Print("Decryption failed\n")
		return "", err
	} else {
		fmt.Print("Decrypted\n")
	}
	// create output file
	ofh, err := os.Create(trackpath)
	if err != nil {
		fmt.Printf("创建文件失败: %v\n", err)
		return "", err
	}
	defer ofh.Close()

	_, err = ofh.Write(buffer.Bytes())
	if err != nil {
		fmt.Printf("写入文件失败: %v\n", err)
		return "", err
	}
	return "", nil
}



type Segment struct {
	Index int
	Data  []byte
}

type DownloadConfig struct {
	Concurrency int           // 最大并发下载数
	MaxRetries  int           // 最大重试次数（不包含第一次）
	RetryDelay  time.Duration // 初始重试等待时间
}

var defaultDownloadConfig = DownloadConfig{
	Concurrency: 5,
	MaxRetries:  3,
	RetryDelay:  time.Second,
}

type segmentJob struct {
	Index int
	URL   string
}

// downloadSegment 下载一个分段。
//
// 下载失败时自动重试，采用指数退避：
// 1s -> 2s -> 4s ...
func downloadSegment(
	ctx context.Context,
	client *http.Client,
	job segmentJob,
	config DownloadConfig,
) (Segment, error) {

	var lastErr error

	for attempt := 0; attempt <= config.MaxRetries; attempt++ {
		if attempt > 0 {
			delay := config.RetryDelay * time.Duration(1<<(attempt-1))

			select {
			case <-ctx.Done():
				return Segment{}, ctx.Err()

			case <-time.After(delay):
			}
		}

		req, err := http.NewRequestWithContext(
			ctx,
			http.MethodGet,
			job.URL,
			nil,
		)
		if err != nil {
			return Segment{}, fmt.Errorf(
				"创建请求失败: %w",
				err,
			)
		}

		resp, err := client.Do(req)
		if err != nil {
			lastErr = fmt.Errorf("HTTP 请求失败: %w", err)
			continue
		}

		// 不要 defer，因为这是循环。
		// defer 会一直累积到函数返回。
		if resp.StatusCode != http.StatusOK {
			_ = resp.Body.Close()

			// 对明确拒绝访问的错误，不盲目快速重试。
			lastErr = fmt.Errorf(
				"服务器返回 HTTP %d",
				resp.StatusCode,
			)

			continue
		}

		data, err := io.ReadAll(resp.Body)
		closeErr := resp.Body.Close()

		if err != nil {
			lastErr = fmt.Errorf(
				"读取响应失败: %w",
				err,
			)
			continue
		}

		if closeErr != nil {
			lastErr = fmt.Errorf(
				"关闭响应失败: %w",
				closeErr,
			)
			continue
		}

		return Segment{
			Index: job.Index,
			Data:  data,
		}, nil
	}

	return Segment{}, fmt.Errorf(
		"分段 %d 下载失败，已重试 %d 次: %w",
		job.Index,
		config.MaxRetries,
		lastErr,
	)
}

// downloadWorker 从 jobs 中获取任务并下载。
func downloadWorker(
	ctx context.Context,
	client *http.Client,
	config DownloadConfig,
	jobs <-chan segmentJob,
	results chan<- Segment,
	errCh chan<- error,
	wg *sync.WaitGroup,
) {
	defer wg.Done()

	for job := range jobs {
		// 如果其他 Worker 已经发生致命错误，尽快退出。
		select {
		case <-ctx.Done():
			return
		default:
		}

		segment, err := downloadSegment(
			ctx,
			client,
			job,
			config,
		)

		if err != nil {
			select {
			case errCh <- fmt.Errorf(
				"分段 %d 下载失败: %w",
				job.Index,
				err,
			):
			default:
			}
			return
		}

		select {
		case results <- segment:

		case <-ctx.Done():
			return
		}
	}
}

// fileWriter 保证按照 Index 顺序写入。
//
// 下载可以乱序完成，但文件必须严格顺序写入。
func fileWriter(
	segments <-chan Segment,
	output io.Writer,
	totalSegments int,
	bar *progressbar.ProgressBar,
) error {

	// 已经下载完成但暂时不能写入的分段。
	buffer := make(map[int][]byte)

	nextIndex := 0

	for segment := range segments {

		// 如果收到了重复分段，忽略即可。
		if segment.Index < nextIndex {
			continue
		}

		// 当前正好是需要写入的分段。
		if segment.Index == nextIndex {

			if _, err := output.Write(segment.Data); err != nil {
				return fmt.Errorf(
					"写入分段 %d 失败: %w",
					segment.Index,
					err,
				)
			}

			if bar != nil {
				_ = bar.Add(len(segment.Data))
			}

			nextIndex++

			// 连续检查 buffer 中是否存在下一个分段。
			for {
				data, ok := buffer[nextIndex]
				if !ok {
					break
				}

				if _, err := output.Write(data); err != nil {
					return fmt.Errorf(
						"写入缓存分段 %d 失败: %w",
						nextIndex,
						err,
					)
				}

				if bar != nil {
					_ = bar.Add(len(data))
				}

				// 写完立即释放内存。
				delete(buffer, nextIndex)

				nextIndex++
			}

			continue
		}

		// 比当前需要的分段更靠后，暂时缓存。
		buffer[segment.Index] = segment.Data
	}

	if nextIndex != totalSegments {
		return fmt.Errorf(
			"下载结束但分段不完整: 期望 %d 个，实际写入 %d 个",
			totalSegments,
			nextIndex,
		)
	}

	return nil
}

// ExtMvData 下载、合并并解密。
func ExtMvData(
	keyAndUrls string,
	savePath string,
) error {

	parts := strings.Split(keyAndUrls, ";")

	if len(parts) < 2 {
		return fmt.Errorf("无效的 keyAndUrls")
	}

	key := parts[0]
	urls := parts[1:]

	if key == "" {
		return fmt.Errorf("解密 Key 不能为空")
	}

	if len(urls) == 0 {
		return fmt.Errorf("没有下载地址")
	}

	// 创建临时加密文件。
	tempFile, err := os.CreateTemp(
		"",
		"enc_mv_data-*.mp4",
	)
	if err != nil {
		return fmt.Errorf(
			"创建临时文件失败: %w",
			err,
		)
	}

	tempFilePath := tempFile.Name()

	defer func() {
		_ = tempFile.Close()
		_ = os.Remove(tempFilePath)
	}()

	config := defaultDownloadConfig

	client := &http.Client{
		Timeout: 60 * time.Second,
	}

	ctx, cancel := context.WithCancel(
		context.Background(),
	)
	defer cancel()

	// Worker Pool。
	workerCount := config.Concurrency
	if workerCount > len(urls) {
		workerCount = len(urls)
	}

	jobs := make(chan segmentJob)

	// 结果 Channel 不需要非常大。
	//
	// 小缓冲可以形成自然的背压，
	// 防止大量分段堆积在内存中。
	results := make(
		chan Segment,
		workerCount*2,
	)

	errCh := make(
		chan error,
		workerCount+1,
	)

	// 初始化进度条。
	bar := progressbar.DefaultBytes(
		-1,
		"Downloading...",
	)

	// -----------------------------
	// 启动 Writer
	// -----------------------------

	writerDone := make(chan error, 1)

	go func() {
		writerDone <- fileWriter(
			results,
			tempFile,
			len(urls),
			bar,
		)
	}()

	// -----------------------------
	// 启动 Worker Pool
	// -----------------------------

	var workerWg sync.WaitGroup

	for i := 0; i < workerCount; i++ {
		workerWg.Add(1)

		go downloadWorker(
			ctx,
			client,
			config,
			jobs,
			results,
			errCh,
			&workerWg,
		)
	}

	// -----------------------------
	// 投递下载任务
	// -----------------------------

	go func() {
		defer close(jobs)

		for index, url := range urls {

			select {
			case <-ctx.Done():
				return

			case jobs <- segmentJob{
				Index: index,
				URL:   url,
			}:
			}
		}
	}()

	// 所有 Worker 退出后关闭结果 Channel。
	go func() {
		workerWg.Wait()
		close(results)
	}()

	// -----------------------------
	// 等待完成或错误
	// -----------------------------

	select {

	case err := <-errCh:
		// 某个分段最终失败。
		cancel()

		// 等待 Writer 正常退出。
		<-writerDone

		return err

	case err := <-writerDone:
		if err != nil {
			cancel()
			return err
		}
	}

	// 关闭临时文件，确保数据全部写入。
	if err := tempFile.Close(); err != nil {
		return fmt.Errorf(
			"关闭临时文件失败: %w",
			err,
		)
	}

	fmt.Println("\nDownloaded.")

	// 确保输出目录存在。
	if err := os.MkdirAll(
		filepath.Dir(savePath),
		0755,
	); err != nil {
		return fmt.Errorf(
			"创建输出目录失败: %w",
			err,
		)
	}

	// 解密。
	cmd := exec.Command(
		"mp4decrypt",
		"--key",
		key,
		tempFilePath,
		filepath.Base(savePath),
	)

	cmd.Dir = filepath.Dir(savePath)

	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf(
			"Decrypt failed: %w\n%s",
			err,
			string(output),
		)
	}

	fmt.Println("Decrypted.")

	return nil
}

// DecryptMP4 decrypts a fragmented MP4 file with keys from widevice license. Supports CENC and CBCS schemes.
func DecryptMP4(r io.Reader, key []byte, w io.Writer) error {
	// Initialization
	inMp4, err := mp4.DecodeFile(r)
	if err != nil {
		return fmt.Errorf("failed to decode file: %w", err)
	}
	if !inMp4.IsFragmented() {
		return errors.New("file is not fragmented")
	}
	// Handle init segment
	if inMp4.Init == nil {
		return errors.New("no init part of file")
	}
	decryptInfo, err := mp4.DecryptInit(inMp4.Init)
	if err != nil {
		return fmt.Errorf("failed to decrypt init: %w", err)
	}
	if err = inMp4.Init.Encode(w); err != nil {
		return fmt.Errorf("failed to write init: %w", err)
	}
	// Decode segments
	for _, seg := range inMp4.Segments {
		if err = mp4.DecryptSegment(seg, decryptInfo, key); err != nil {
			if err.Error() == "no senc box in traf" {
				// No SENC box, skip decryption for this segment as samples can have
				// unencrypted segments followed by encrypted segments. See:
				// https://github.com/iyear/gowidevine/pull/26#issuecomment-2385960551
				err = nil
			} else {
				return fmt.Errorf("failed to decrypt segment: %w", err)
			}
		}
		if err = seg.Encode(w); err != nil {
			return fmt.Errorf("failed to encode segment: %w", err)
		}
	}
	return nil
}
