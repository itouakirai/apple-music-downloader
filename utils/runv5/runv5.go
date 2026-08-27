package runv5

import (
	"context"
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"path/filepath"

	"github.com/go-resty/resty/v2"
	"google.golang.org/protobuf/proto"

	cdm "main/utils/runv3/cdm"
	key "main/utils/runv3/key"
	"os"

	"bytes"
	"errors"
	"io"

	"github.com/itouakirai/mp4ff/mp4"

	"encoding/json"
	"net/http"
	"net/url"
	"os/exec"
	"strings"
	"sync"

	"github.com/grafov/m3u8"
	"github.com/schollz/progressbar/v3"

	"main/utils/httputil"
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
	// lite-server 返回的可能是 master 也可能是 media playlist，
	// 先解析成 AAC media playlist，再像 runv3 一样提取 KID / uriPrefix。
	mediaURL, err := resolveToMediaPlaylist(envelope.Data.M3u8)
	if err != nil {
		return "", "", "", err
	}
	kidBase64, fileurl, uriPrefix, err := extractKidBase64(mediaURL, false)
	if err != nil {
		return "", "", "", err
	}
	return fileurl, kidBase64, uriPrefix, nil
}

// resolveToMediaPlaylist 下载 b；如果它是 master playlist，则挑选 AAC
// (ctrp256 / mp4a.40.2) 变体并返回其绝对 URL；已经是 media playlist 时原样返回。
func resolveToMediaPlaylist(b string) (string, error) {
	body, err := getURLWithHeaders(b, "", "")
	if err != nil {
		return "", err
	}
	masterString := string(body)
	from, listType, err := m3u8.DecodeFrom(strings.NewReader(masterString), true)
	if err != nil {
		return "", err
	}
	if listType != m3u8.MASTER {
		return b, nil
	}
	masterPlaylist := from.(*m3u8.MasterPlaylist)
	var preferred string
	for _, variant := range masterPlaylist.Variants {
		if variant == nil {
			continue
		}
		uri := variant.URI
		if strings.Contains(uri, "ctrp256") || strings.Contains(uri, "256_6") ||
			strings.Contains(variant.Codecs, "mp4a.40.2") {
			preferred = uri
			break
		}
		if preferred == "" {
			preferred = uri
		}
	}
	if preferred == "" {
		return "", errors.New("no AAC variant found in master playlist")
	}
	if strings.HasPrefix(preferred, "http") {
		return preferred, nil
	}
	lastSlashIndex := strings.LastIndex(b, "/")
	if lastSlashIndex == -1 {
		return "", errors.New("cannot resolve relative variant URI")
	}
	return b[:lastSlashIndex+1] + preferred, nil
}


// parsePsshHeader 从标准 pssh box 中取出 WidevineCencHeader protobuf。
func parsePsshHeader(box []byte) ([]byte, error) {
	if len(box) < 32 || string(box[4:8]) != "pssh" {
		return nil, errors.New("not a pssh box")
	}
	// box: size(4) type(4) version/flags(4) system id(16) data size(4) data
	dataSize := binary.BigEndian.Uint32(box[28:32])
	if 32+int(dataSize) > len(box) {
		return nil, errors.New("invalid pssh box")
	}
	return box[32 : 32+int(dataSize)], nil
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

// Segment 结构体用于在 Channel 中传递分段数据
type Segment struct {
	Index int
	Data  []byte
}

func downloadSegment(url string, index int, wg *sync.WaitGroup, segmentsChan chan<- Segment, client *http.Client, limiter chan struct{}) {
	// 函数退出时，从 limiter 中接收一个值，释放一个并发槽位
	defer func() {
		<-limiter
		wg.Done()
	}()

	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		fmt.Printf("错误(分段 %d): 创建请求失败: %v\n", index, err)
		return
	}

	resp, err := client.Do(req)
	if err != nil {
		fmt.Printf("错误(分段 %d): 下载失败: %v\n", index, err)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		fmt.Printf("错误(分段 %d): 服务器返回状态码 %d\n", index, resp.StatusCode)
		return
	}

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		fmt.Printf("错误(分段 %d): 读取数据失败: %v\n", index, err)
		return
	}

	// 将下载好的分段（包含序号和数据）发送到 Channel
	segmentsChan <- Segment{Index: index, Data: data}
}

// fileWriter 从 Channel 接收分段并按顺序写入文件
func fileWriter(wg *sync.WaitGroup, segmentsChan <-chan Segment, outputFile io.Writer, totalSegments int) {
	defer wg.Done()

	// 缓冲区，用于存放乱序到达的分段
	// key 是分段序号，value 是分段数据
	segmentBuffer := make(map[int][]byte)
	nextIndex := 0 // 期望写入的下一个分段的序号

	for segment := range segmentsChan {
		// 检查收到的分段是否是当前期望的
		if segment.Index == nextIndex {
			//fmt.Printf("写入分段 %d\n", segment.Index)
			_, err := outputFile.Write(segment.Data)
			if err != nil {
				fmt.Printf("错误(分段 %d): 写入文件失败: %v\n", segment.Index, err)
			}
			nextIndex++

			// 检查缓冲区中是否有下一个连续的分段
			for {
				data, ok := segmentBuffer[nextIndex]
				if !ok {
					break // 缓冲区里没有下一个，跳出循环，等待下一个分段到达
				}

				//fmt.Printf("从缓冲区写入分段 %d\n", nextIndex)
				_, err := outputFile.Write(data)
				if err != nil {
					fmt.Printf("错误(分段 %d): 从缓冲区写入文件失败: %v\n", nextIndex, err)
				}
				// 从缓冲区删除已写入的分段，释放内存
				delete(segmentBuffer, nextIndex)
				nextIndex++
			}
		} else {
			// 如果不是期望的分段，先存入缓冲区
			//fmt.Printf("缓冲分段 %d (等待 %d)\n", segment.Index, nextIndex)
			segmentBuffer[segment.Index] = segment.Data
		}
	}

	// 确保所有分段都已写入
	if nextIndex != totalSegments {
		fmt.Printf("警告: 写入完成，但似乎有分段丢失。期望 %d 个, 实际写入 %d 个。\n", totalSegments, nextIndex)
	}
}

func ExtMvData(keyAndUrls string, savePath string) error {
	segments := strings.Split(keyAndUrls, ";")
	key := segments[0]
	//fmt.Println(key)
	urls := segments[1:]
	tempFile, err := os.CreateTemp("", "enc_mv_data-*.mp4")
	if err != nil {
		fmt.Printf("创建文件失败：%v\n", err)
		return err
	}
	defer os.Remove(tempFile.Name())
	defer tempFile.Close()

	var downloadWg, writerWg sync.WaitGroup
	segmentsChan := make(chan Segment, len(urls))
	// --- 新增代码: 定义最大并发数 ---
	const maxConcurrency = 10
	// --- 新增代码: 创建带缓冲的 Channel 作为信号量 ---
	limiter := make(chan struct{}, maxConcurrency)
	client := &http.Client{}

	// 初始化进度条
	bar := progressbar.DefaultBytes(-1, "Downloading...")
	barWriter := io.MultiWriter(tempFile, bar)

	// 启动写入 Goroutine
	writerWg.Add(1)
	go fileWriter(&writerWg, segmentsChan, barWriter, len(urls))

	// 启动下载 Goroutines
	for i, url := range urls {
		// 在启动 Goroutine 前，向 limiter 发送一个值来“获取”一个槽位
		// 如果 limiter 已满 (达到10个)，这里会阻塞，直到有其他任务完成并释放槽位
		//fmt.Printf("请求启动任务 %d...\n", i)
		limiter <- struct{}{}
		//fmt.Printf("...任务 %d 已启动\n", i)

		downloadWg.Add(1)
		// 将 limiter 传递给下载函数
		go downloadSegment(url, i, &downloadWg, segmentsChan, client, limiter)
	}

	// 等待所有下载任务完成
	downloadWg.Wait()
	// 下载完成后，关闭 Channel。写入 Goroutine 会在处理完 Channel 中所有数据后退出。
	close(segmentsChan)

	// 等待写入 Goroutine 完成所有写入和缓冲处理
	writerWg.Wait()

	// 显式关闭文件（defer会再次调用，但重复关闭是安全的）
	if err := tempFile.Close(); err != nil {
		fmt.Printf("关闭临时文件失败: %v\n", err)
		return err
	}
	fmt.Println("\nDownloaded.")

	cmd1 := exec.Command("mp4decrypt", "--key", key, tempFile.Name(), filepath.Base(savePath))
	cmd1.Dir = filepath.Dir(savePath) //设置mp4decrypt的工作目录以解决中文路径错误
	outlog, err := cmd1.CombinedOutput()
	if err != nil {
		fmt.Printf("Decrypt failed: %v\n", err)
		fmt.Printf("Output:\n%s\n", outlog)
		return err
	} else {
		fmt.Println("Decrypted.")
	}
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
