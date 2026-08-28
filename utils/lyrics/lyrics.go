package lyrics

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/beevik/etree"
	//"main/utils/httputil"
)

func Get(songId, lrcType, language, lrcFormat, liteServer, lrcExtra string) (string, error) {
	ttml, err := getSongLyrics(songId, liteServer, lrcType, language)
	if err != nil {
		return "", err
	}

	if lrcFormat == "ttml" {
		return ttml, nil
	}

	lrc, err := TtmlToLrc(ttml, lrcExtra)
	if err != nil {
		return "", err
	}

	return lrc, nil
}

func getSongLyrics(songId string, liteServer string, lrcType string, language string) (string, error) {
	if liteServer == "" {
		return "", errors.New("lite-server is not configured")
	}
	isSyllable := "1"
	if lrcType == "lyrics" {
		isSyllable = "0"
	}
	endpoint := strings.TrimRight(liteServer, "/") + "/lyrics?adamId=" + url.QueryEscape(songId) + "&language=" + url.QueryEscape(language) + "&syllable=" + isSyllable
	resp, err := http.Get(endpoint)
	if err != nil {
		fmt.Println("Error connecting to lite-server:", err)
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", errors.New(resp.Status)
	}
	var envelope struct {
		Code int    `json:"code"`
		Msg  string `json:"msg"`
		Data struct {
			Lyrics string `json:"lyrics"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&envelope); err != nil {
		return "", err
	}
	if envelope.Code != 0 {
		return "", fmt.Errorf("lite-server /lyrics returned code=%d msg=%s", envelope.Code, envelope.Msg)
	}
	return envelope.Data.Lyrics ,nil
}

func TtmlToLrc(ttml string, lyricsExtra string) (string, error) {
	//lyricsExtra := "pronunciation" //"pronunciation" "translation"
	parsedTTML := etree.NewDocument()
	err := parsedTTML.ReadFromString(ttml)
	if err != nil {
		return "", err
	}

	var lrcLines []string
	var transType = ""
	timingAttr := parsedTTML.FindElement("tt").SelectAttr("itunes:timing")
	if timingAttr != nil {
		if timingAttr.Value == "Word" {
			lrc, err := conventSyllableTTMLToLRC(ttml, lyricsExtra)
			return lrc, err
		}
		if timingAttr.Value == "None" {
			for _, p := range parsedTTML.FindElements("//p") {
				line := p.Text()
				line = strings.TrimSpace(line)
				if line != "" {
					lrcLines = append(lrcLines, line)
				}
			}
			return strings.Join(lrcLines, "\n"), nil
		}
	}

	for _, item := range parsedTTML.FindElement("tt").FindElement("body").ChildElements() {
		for _, lyric := range item.ChildElements() {
			var h, m, s, ms int
			beginAttr := lyric.SelectAttr("begin")
			if beginAttr == nil {
				return "", errors.New("no synchronised lyrics")
			}
			beginValue := beginAttr.Value
			if strings.Contains(beginValue, ":") {
				_, err = fmt.Sscanf(beginValue, "%d:%d:%d.%d", &h, &m, &s, &ms)
				if err != nil {
					_, err = fmt.Sscanf(beginValue, "%d:%d.%d", &m, &s, &ms)
					if err != nil {
						_, err = fmt.Sscanf(beginValue, "%d:%d", &m, &s)
					}
					h = 0
				}
			} else {
				_, err = fmt.Sscanf(beginValue, "%d.%d", &s, &ms)
				h, m = 0, 0
			}
			if err != nil {
				return "", err
			}
			m += h * 60
			ms = ms / 10
			var text, transText, translitText string
			//GET trans and translit
			if len(parsedTTML.FindElement("tt").FindElements("head")) > 0 {
				if len(parsedTTML.FindElement("tt").FindElement("head").FindElements("metadata")) > 0 {
					Metadata := parsedTTML.FindElement("tt").FindElement("head").FindElement("metadata")
					if len(Metadata.FindElements("iTunesMetadata")) > 0 {
						iTunesMetadata := Metadata.FindElement("iTunesMetadata")
						if len(iTunesMetadata.FindElements("transliterations")) > 0 {
							if len(iTunesMetadata.FindElement("transliterations").FindElements("transliteration")) > 0 {
								xpath := fmt.Sprintf("text[@for='%s']", lyric.SelectAttr("itunes:key").Value)
								translit := iTunesMetadata.FindElement("transliterations").FindElement("transliteration").FindElement(xpath)
								if translit != nil {
									if translit.SelectAttr("text") != nil {
										translitText = translit.SelectAttr("text").Value
									} else {
										var translitTmp []string
										for _, span := range translit.Child {
											if c, ok := span.(*etree.CharData); ok {
												translitTmp = append(translitTmp, c.Data)
											} else if e, ok := span.(*etree.Element); ok {
												translitTmp = append(translitTmp, e.Text())
											}
										}
										translitText = strings.Join(translitTmp, "")
									}
								}
							}
						}
						if len(iTunesMetadata.FindElements("translations")) > 0 {
							if len(iTunesMetadata.FindElement("translations").FindElements("translation")) > 0 {
								xpath := fmt.Sprintf("//text[@for='%s']", lyric.SelectAttr("itunes:key").Value)
								trans := iTunesMetadata.FindElement("translations").FindElement("translation").FindElement(xpath)
								if transType == "" {
									transType = iTunesMetadata.FindElement("translations").FindElement("translation").SelectAttr("type").Value
								}
								if trans != nil {
									if trans.SelectAttr("text") != nil {
										transText = trans.SelectAttr("text").Value
									} else {
										var transTmp []string
										for _, span := range trans.Child {
											if c, ok := span.(*etree.CharData); ok {
												transTmp = append(transTmp, c.Data)
											} else if e, ok := span.(*etree.Element); ok {
												transTmp = append(transTmp, e.Text())
											}
										}
										transText = strings.Join(transTmp, "")
									}
								}
							}
						}
					}
				}
			}
			if lyric.SelectAttr("text") == nil {
				var textTmp []string
				for _, span := range lyric.Child {
					if _, ok := span.(*etree.CharData); ok {
						textTmp = append(textTmp, span.(*etree.CharData).Data)
					} else {
						textTmp = append(textTmp, span.(*etree.Element).Text())
					}
				}
				text = strings.Join(textTmp, "")
			} else {
				text = lyric.SelectAttr("text").Value
			}

			if len(transText) > 0 && transType == "replacement" {
				lrcLines = append(lrcLines, fmt.Sprintf("[%02d:%02d.%02d]%s", m, s, ms, transText))
			} else {
				lrcLines = append(lrcLines, fmt.Sprintf("[%02d:%02d.%02d]%s", m, s, ms, text))
			}
			if len(translitText) > 0 && lyricsExtra == "pronunciation" {
				lrcLines = append(lrcLines, fmt.Sprintf("[%02d:%02d.%02d]%s", m, s, ms, translitText))
			} else if len(transText) > 0 && transType == "subtitle" && lyricsExtra == "translation" {
				lrcLines = append(lrcLines, fmt.Sprintf("[%02d:%02d.%02d]%s", m, s, ms, transText))
			}
		}
	}
	return strings.Join(lrcLines, "\n"), nil
}

func conventSyllableTTMLToLRC(ttml string, lyricsExtra string) (string, error) {
	parsedTTML := etree.NewDocument()
	err := parsedTTML.ReadFromString(ttml)
	if err != nil {
		return "", err
	}
	var lrcLines []string
	parseTime := func(timeValue string, newLine int) (string, error) {
		var h, m, s, ms int
		if strings.Contains(timeValue, ":") {
			_, err = fmt.Sscanf(timeValue, "%d:%d:%d.%d", &h, &m, &s, &ms)
			if err != nil {
				_, err = fmt.Sscanf(timeValue, "%d:%d.%d", &m, &s, &ms)
				h = 0
			}
		} else {
			_, err = fmt.Sscanf(timeValue, "%d.%d", &s, &ms)
			h, m = 0, 0
		}
		if err != nil {
			return "", err
		}
		m += h * 60
		ms = ms / 10
		if newLine == 0 {
			return fmt.Sprintf("[%02d:%02d.%02d]<%02d:%02d.%02d>", m, s, ms, m, s, ms), nil
		} else if newLine == -1 {
			return fmt.Sprintf("[%02d:%02d.%02d]", m, s, ms), nil
		} else {
			return fmt.Sprintf("<%02d:%02d.%02d>", m, s, ms), nil
		}
	}
	divs := parsedTTML.FindElement("tt").FindElement("body").FindElements("div")
	var transType = ""
	for _, div := range divs {
		for _, item := range div.ChildElements() {     //LINES
			var lrcSyllables []string
			var i int = 0
			var endTime, translitLine, transLine string
			for _, lyrics := range item.Child {    //WORDS
				if _, ok := lyrics.(*etree.CharData); ok {   //是否为span之间的空格
					if i > 0 {
						lrcSyllables = append(lrcSyllables, " ")
						continue
					}
					continue
				}
				lyric := lyrics.(*etree.Element)
				if lyric.SelectAttr("begin") == nil {
					continue
				}
				beginTime, err := parseTime(lyric.SelectAttr("begin").Value, i)
				if err != nil {
					return "", err
				}

				endTime, err = parseTime(lyric.SelectAttr("end").Value, 1)
				if err != nil {
					return "", err
				}
				var text string
				if lyric.SelectAttr("text") == nil {
					var textTmp []string
					for _, span := range lyric.Child {
						if _, ok := span.(*etree.CharData); ok {
							textTmp = append(textTmp, span.(*etree.CharData).Data)
						} else {
							textTmp = append(textTmp, span.(*etree.Element).Text())
						}
					}
					text = strings.Join(textTmp, "")
				} else {
					text = lyric.SelectAttr("text").Value
				}
				lrcSyllables = append(lrcSyllables, fmt.Sprintf("%s%s", beginTime, text))
				if i == 0 {
					transBeginTime, _ := parseTime(lyric.SelectAttr("begin").Value, -1)
					sharedTimestamp := ""
					if len(parsedTTML.FindElement("tt").FindElements("head")) > 0 {
						if len(parsedTTML.FindElement("tt").FindElement("head").FindElements("metadata")) > 0 {
							Metadata := parsedTTML.FindElement("tt").FindElement("head").FindElement("metadata")
							if len(Metadata.FindElements("iTunesMetadata")) > 0 {
								iTunesMetadata := Metadata.FindElement("iTunesMetadata")
								if len(iTunesMetadata.FindElements("transliterations")) > 0 {
									if len(iTunesMetadata.FindElement("transliterations").FindElements("transliteration")) > 0 {
										xpath := fmt.Sprintf("text[@for='%s']", item.SelectAttr("itunes:key").Value)
										trans := iTunesMetadata.FindElement("transliterations").FindElement("transliteration").FindElement(xpath)
										// Get text content
										var transTxtParts []string
										var transStartTime string
										for i, span := range trans.ChildElements() {
											if span.Tag == "span" {
												spanBegin := span.SelectAttrValue("begin", "")
												spanText := span.Text()
												if spanBegin == "" {
													continue
												}
												// Get timestamp
												timestamp, err := parseTime(spanBegin, 2)
												if err != nil {
													return "", err
												}
												if i == 0 {
													// For [mm:ss.xx] prefix
													transStartTime, _ = parseTime(spanBegin, -1)
													sharedTimestamp = transStartTime
												}
												transTxtParts = append(transTxtParts, fmt.Sprintf("%s%s", timestamp, spanText))
											}
										}
										translitLine = fmt.Sprintf("%s%s", transStartTime, strings.Join(transTxtParts, " "))
									}
								}
								if len(iTunesMetadata.FindElements("translations")) > 0 {
									if len(iTunesMetadata.FindElement("translations").FindElements("translation")) > 0 {
										xpath := fmt.Sprintf("//text[@for='%s']", item.SelectAttr("itunes:key").Value)
										trans := iTunesMetadata.FindElement("translations").FindElement("translation").FindElement(xpath)
										if transType == "" {
											transType = iTunesMetadata.FindElement("translations").FindElement("translation").SelectAttr("type").Value
										}
										if transType == "subtitle" {
											var transTxt string
											if trans.SelectAttr("text") == nil {
												var textTmp []string
												for _, span := range trans.Child {
													if _, ok := span.(*etree.CharData); ok {
														textTmp = append(textTmp, span.(*etree.CharData).Data)
													} /*else {
													textTmp = append(textTmp, span.(*etree.Element).Text())
												}*/
												}
												transTxt = strings.Join(textTmp, "")
											} else {
												transTxt = trans.SelectAttr("text").Value
											}
											//fmt.Println(transTxt)
											if sharedTimestamp != "" {
												transLine = sharedTimestamp + transTxt
											} else {
												transLine = transBeginTime + transTxt
											}
										} else if transType == "replacement"{
											// Get text content
											var transTxtParts []string
											var transStartTime string
											for i, span := range trans.ChildElements() {
												if span.Tag == "span" {
													spanBegin := span.SelectAttrValue("begin", "")
													spanText := span.Text()
													if spanBegin == "" {
														continue
													}
													// Get timestamp
													timestamp, err := parseTime(spanBegin, 2)
													if err != nil {
														return "", err
													}
													if i == 0 {
														// For [mm:ss.xx] prefix
														transStartTime, _ = parseTime(spanBegin, -1)
														//_ = transStartTime
													}
													transTxtParts = append(transTxtParts, fmt.Sprintf("%s%s", timestamp, spanText))
												}
											}
											transLine = fmt.Sprintf("%s%s", transStartTime, strings.Join(transTxtParts, " "))
										}

									}
								}
							}
						}
					}
				}
				i += 1
			}
			//endTime, err := parseTime(item.SelectAttr("end").Value)
			//if err != nil {
			//	return "", err
			//}
			// fmt.Println(transLine)
			// fmt.Println(translitLine)
			// fmt.Println(lrcSyllables)
			// fmt.Println(transType)
			if len(transLine) > 0 && transType == "replacement" {
				lrcLines = append(lrcLines, transLine)
			} else {
				lrcLines = append(lrcLines, strings.Join(lrcSyllables, "")+endTime)
			}
			if len(translitLine) > 0 && lyricsExtra == "pronunciation" {
				lrcLines = append(lrcLines, translitLine)
			} else if len(transLine) > 0 && transType == "subtitle" && lyricsExtra == "translation" {
				lrcLines = append(lrcLines, transLine)
			}
		}
	}
	return strings.Join(lrcLines, "\n"), nil
}
