package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const vkAPIVersion = "5.282"

var (
	vkNumericIDPattern = regexp.MustCompile(`^[1-9][0-9]{0,19}$`)
	vkProfileIDPattern = regexp.MustCompile(`(?i)^(?:https?://)?(?:www\.)?vk\.com/id([1-9][0-9]{0,19})/?$`)
)

func normalizeVKRecipient(raw string) (string, error) {
	value := strings.TrimSpace(raw)
	if match := vkProfileIDPattern.FindStringSubmatch(value); len(match) == 2 {
		value = match[1]
	}
	if !vkNumericIDPattern.MatchString(value) {
		return "", errors.New("VK recipient must be a numeric ID or vk.com/id123 link")
	}
	if _, err := strconv.ParseUint(value, 10, 63); err != nil {
		return "", errors.New("VK recipient is out of range")
	}
	return value, nil
}

func readVKCookieHeader(path string) (string, error) {
	body, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	var cookies []vkStoredCookie
	if err := json.Unmarshal(body, &cookies); err != nil {
		return "", errors.New("invalid VK cookie file")
	}
	parts := make([]string, 0, len(cookies))
	for _, cookie := range cookies {
		if cookie.Name != "" && cookie.Value != "" {
			parts = append(parts, cookie.Name+"="+cookie.Value)
		}
	}
	if len(parts) == 0 {
		return "", errors.New("VK cookie file is empty")
	}
	return strings.Join(parts, "; "), nil
}

func fetchVKAccessToken(parent context.Context, cookieHeader string) (string, error) {
	ctx, cancel := context.WithTimeout(parent, 12*time.Second)
	defer cancel()
	form := url.Values{"version": {"1"}, "app_id": {vkCallsAppID}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, vkWebTokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("User-Agent", vkLoginUserAgent)
	req.Header.Set("Origin", "https://vk.com")
	req.Header.Set("Referer", "https://vk.com/")
	req.Header.Set("Cookie", cookieHeader)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	var result struct {
		Data struct {
			AccessToken string `json:"access_token"`
		} `json:"data"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&result); err != nil {
		return "", err
	}
	if result.Data.AccessToken == "" {
		return "", errors.New("VK did not accept the server session")
	}
	return result.Data.AccessToken, nil
}

func sendVKTestMessage(ctx context.Context, cookiePath, recipient, message string) error {
	cookies, err := readVKCookieHeader(cookiePath)
	if err != nil {
		return err
	}
	token, err := fetchVKAccessToken(ctx, cookies)
	if err != nil {
		return err
	}
	form := url.Values{
		"v": {vkAPIVersion}, "peer_id": {recipient}, "message": {message},
		"random_id": {strconv.FormatInt(time.Now().UnixNano()&0x7fffffff, 10)},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://api.vk.com/method/messages.send", strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	var result struct {
		Error *struct {
			Code int    `json:"error_code"`
			Text string `json:"error_msg"`
		} `json:"error"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&result); err != nil {
		return err
	}
	if result.Error != nil {
		return fmt.Errorf("VK messages.send error %d", result.Error.Code)
	}
	return nil
}
