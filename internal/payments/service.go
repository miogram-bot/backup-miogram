package payments

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"miogram/internal/config"
	"miogram/internal/storage"
	"miogram/internal/telegram"
)

type Service struct {
	cfg   config.Config
	store *storage.Store
	tg    *telegram.Client
	http  *http.Client
}

func New(cfg config.Config, store *storage.Store, tg *telegram.Client) *Service {
	return &Service{cfg: cfg, store: store, tg: tg, http: &http.Client{Timeout: cfg.HTTPTimeout}}
}

func (s *Service) Request(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id := r.URL.Query().Get("id")
	if id == "" {
		http.NotFound(w, r)
		return
	}
	p, ok, err := s.payment(ctx, id, `status IN ('first_level','move_gateway')`)
	if err != nil {
		http.Error(w, "خطای ناشناخته ای رخ داد !", http.StatusInternalServerError)
		return
	}
	if !ok {
		http.NotFound(w, r)
		return
	}
	switch s.cfg.PaymentGateway {
	case "idpay":
		s.idpayRequest(w, r, p)
	default:
		s.zarinpalRequest(w, r, p)
	}
}

func (s *Service) Verify(w http.ResponseWriter, r *http.Request) {
	switch s.cfg.PaymentGateway {
	case "idpay":
		s.idpayVerify(w, r)
	default:
		s.zarinpalVerify(w, r)
	}
}

func (s *Service) zarinpalRequest(w http.ResponseWriter, r *http.Request, p storage.Payment) {
	payload := map[string]any{
		"merchant_id":  s.cfg.MerchantID,
		"amount":       p.Amount * 10,
		"callback_url": s.callbackURL(),
		"description":  "توضیحات سفارش",
	}
	var result struct {
		Data struct {
			Code      int    `json:"code"`
			Authority string `json:"authority"`
		} `json:"data"`
	}
	if err := s.postJSON(r.Context(), "https://api.zarinpal.com/pg/v4/payment/request.json", payload, nil, &result); err != nil || result.Data.Code != 100 {
		_, _ = io.WriteString(w, "خطایی رخ داد !")
		return
	}
	_, _ = s.store.DB().Exec(r.Context(), `UPDATE payments SET authority=$2,status='move_gateway',updated_at=$3 WHERE id=$1`, p.ID, result.Data.Authority, time.Now().Unix())
	http.Redirect(w, r, "https://www.zarinpal.com/pg/StartPay/"+result.Data.Authority, http.StatusFound)
}

func (s *Service) zarinpalVerify(w http.ResponseWriter, r *http.Request) {
	if r.URL.Query().Get("Authority") == "" || r.URL.Query().Get("Status") != "OK" {
		return
	}
	authority := r.URL.Query().Get("Authority")
	p, ok, err := s.paymentByAuthority(r.Context(), authority)
	if err != nil || !ok {
		_, _ = io.WriteString(w, "خطایی رخ داد !\ncode = 131")
		return
	}
	payload := map[string]any{"merchant_id": s.cfg.MerchantID, "authority": authority, "amount": p.Amount * 10}
	var result struct {
		Data struct {
			Code   int    `json:"code"`
			RefID  string `json:"ref_id"`
			Amount int    `json:"amount"`
		} `json:"data"`
	}
	if err := s.postJSON(r.Context(), "https://api.zarinpal.com/pg/v4/payment/verify.json", payload, nil, &result); err == nil && (result.Data.Code == 100 || result.Data.Code == 101) {
		if result.Data.Amount != p.Amount*10 {
			log.Printf("payment amount mismatch for %d: expected %d, gateway returned %d", p.ID, p.Amount*10, result.Data.Amount)
			_, _ = io.WriteString(w, "مبلغ پرداخت شده با مبلغ درخواستی مطابقت ندارد!")
			return
		}
		s.success(r.Context(), p, result.Data.RefID)
		_, _ = io.WriteString(w, fmt.Sprintf("تراکنش با موفقیت پرداخت شد !<br>کد پیگیری : %d", p.TrackingNumber))
		return
	}
	_, _ = io.WriteString(w, fmt.Sprintf("مشکلی در تایید تراکنش رخ داد !<br>کد پیگیری : %d", p.TrackingNumber))
}

func (s *Service) idpayRequest(w http.ResponseWriter, r *http.Request, p storage.Payment) {
	payload := map[string]any{
		"order_id": p.UniqID,
		"amount":   p.Amount * 10,
		"name":     "no name",
		"phone":    "09121234567",
		"mail":     "email@gmail.com",
		"desc":     "توضیحات سفارش",
		"callback": s.callbackURL(),
		"reseller": nil,
	}
	var result struct {
		ID   string `json:"id"`
		Link string `json:"link"`
	}
	headers := map[string]string{"X-API-KEY": s.cfg.MerchantID}
	if err := s.postJSON(r.Context(), "https://api.idpay.ir/v1.1/payment", payload, headers, &result); err != nil || result.ID == "" || result.Link == "" {
		_, _ = io.WriteString(w, "خطایی رخ داد !")
		return
	}
	_, _ = s.store.DB().Exec(r.Context(), `UPDATE payments SET authority=$2,status='move_gateway',updated_at=$3 WHERE id=$1`, p.ID, result.ID, time.Now().Unix())
	http.Redirect(w, r, result.Link, http.StatusFound)
}

func (s *Service) idpayVerify(w http.ResponseWriter, r *http.Request) {
	if r.URL.Query().Get("id") == "" || r.URL.Query().Get("order_id") == "" || r.URL.Query().Get("status") == "" {
		return
	}
	authority := r.URL.Query().Get("id")
	p, ok, err := s.paymentByAuthority(r.Context(), authority)
	if err != nil || !ok {
		_, _ = io.WriteString(w, "خطایی رخ داد !\ncode = 131")
		return
	}
	payload := map[string]any{"id": authority, "order_id": p.UniqID}
	var result struct {
		Status  int    `json:"status"`
		TrackID string `json:"track_id"`
		Amount  int    `json:"amount"`
	}
	headers := map[string]string{"X-API-KEY": s.cfg.MerchantID}
	if err := s.postJSON(r.Context(), "https://api.idpay.ir/v1.1/payment/verify", payload, headers, &result); err == nil && (result.Status == 100 || result.Status == 101) {
		if result.Amount != p.Amount*10 {
			log.Printf("payment amount mismatch for %d: expected %d, gateway returned %d", p.ID, p.Amount*10, result.Amount)
			_, _ = io.WriteString(w, "مبلغ پرداخت شده با مبلغ درخواستی مطابقت ندارد!")
			return
		}
		s.success(r.Context(), p, result.TrackID)
		_, _ = io.WriteString(w, fmt.Sprintf("تراکنش با موفقیت پرداخت شد !<br>کد پیگیری : %d", p.TrackingNumber))
		return
	}
	_, _ = io.WriteString(w, fmt.Sprintf("مشکلی در تایید تراکنش رخ داد !<br>کد پیگیری : %d", p.TrackingNumber))
}

func (s *Service) success(ctx context.Context, p storage.Payment, refID string) {
	now := time.Now().Unix()
	tx, err := s.store.DB().Begin(ctx)
	if err != nil {
		return
	}
	defer func() { _ = tx.Rollback(ctx) }()
	tag, err := tx.Exec(ctx, `UPDATE payments SET ref_id=$2,status='success',updated_at=$3 WHERE id=$1 AND status='move_gateway'`, p.ID, refID, now)
	if err != nil || tag.RowsAffected() == 0 {
		return
	}
	if _, err = tx.Exec(ctx, `UPDATE users SET balance=balance+$2 WHERE user_id=$1`, p.UserID, p.Coins); err != nil {
		return
	}
	if err = tx.Commit(ctx); err != nil {
		return
	}
	_, _ = s.tg.Call(ctx, "sendMessage", map[string]any{
		"chat_id":                  p.UserID,
		"text":                     fmt.Sprintf("✅ تراکنش با کد پیگیری <code>%d</code> با موفقیت پرداخت شد.\n\n🌟 تعداد %d سکه به حساب شما افزوده شد.\n\nاز پرداخت شما متشکریم 🌹\n‌", p.TrackingNumber, p.Coins),
		"parse_mode":               "html",
		"disable_web_page_preview": true,
	})
}

func (s *Service) payment(ctx context.Context, id, statusWhere string) (storage.Payment, bool, error) {
	var p storage.Payment
	query := `SELECT id,user_id,coins,amount,coalesce(authority,''),coalesce(ref_id,''),status,created_at,updated_at,coalesce(uniq_id,''),tracking_number FROM payments WHERE id=$1 AND ` + statusWhere
	err := s.store.DB().QueryRow(ctx, query, id).Scan(&p.ID, &p.UserID, &p.Coins, &p.Amount, &p.Authority, &p.RefID, &p.Status, &p.CreatedAt, &p.UpdatedAt, &p.UniqID, &p.TrackingNumber)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return p, false, nil
		}
		return p, false, err
	}
	return p, true, nil
}

func (s *Service) paymentByAuthority(ctx context.Context, authority string) (storage.Payment, bool, error) {
	var p storage.Payment
	err := s.store.DB().QueryRow(ctx, `SELECT id,user_id,coins,amount,coalesce(authority,''),coalesce(ref_id,''),status,created_at,updated_at,coalesce(uniq_id,''),tracking_number FROM payments WHERE authority=$1 AND status='move_gateway'`, authority).
		Scan(&p.ID, &p.UserID, &p.Coins, &p.Amount, &p.Authority, &p.RefID, &p.Status, &p.CreatedAt, &p.UpdatedAt, &p.UniqID, &p.TrackingNumber)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return p, false, nil
		}
		return p, false, err
	}
	return p, true, nil
}

func (s *Service) callbackURL() string {
	base := strings.TrimRight(s.cfg.PublicBaseURL, "/")
	if base == "" {
		return "/pay/verify.php"
	}
	return base + "/pay/verify.php"
}

func (s *Service) postJSON(ctx context.Context, endpoint string, payload any, headers map[string]string, out any) error {
	raw, _ := json.Marshal(payload)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(raw))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Content-Length", strconv.Itoa(len(raw)))
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := s.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	return json.Unmarshal(body, out)
}

// ReVerifyPayment re-checks a payment still in 'move_gateway' (e.g. the user
// paid at the gateway but the browser redirect never reached us). It replays the
// gateway verification server-side and credits on success.
func (s *Service) ReVerifyPayment(ctx context.Context, paymentID int64) error {
	p, ok, err := s.payment(ctx, strconv.FormatInt(paymentID, 10), "status='move_gateway'")
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("payment %d is not pending verification", paymentID)
	}
	refID, verr := s.reverifyGateway(ctx, p)
	if verr != nil {
		return verr
	}
	s.success(ctx, p, refID)
	return nil
}

// reverifyGateway replays the provider's verify call for an existing payment.
func (s *Service) reverifyGateway(ctx context.Context, p storage.Payment) (string, error) {
	switch s.cfg.PaymentGateway {
	case "idpay":
		payload := map[string]any{"id": p.Authority, "order_id": p.UniqID}
		headers := map[string]string{"X-API-KEY": s.cfg.MerchantID}
		var result struct {
			Status  int    `json:"status"`
			TrackID string `json:"track_id"`
			Amount  int    `json:"amount"`
		}
		if err := s.postJSON(ctx, "https://api.idpay.ir/v1.1/payment/verify", payload, headers, &result); err != nil || (result.Status != 100 && result.Status != 101) {
			return "", fmt.Errorf("idpay re-verify failed for %d", p.ID)
		}
		if result.Amount != p.Amount*10 {
			return "", fmt.Errorf("idpay amount mismatch for %d", p.ID)
		}
		return result.TrackID, nil
	default:
		payload := map[string]any{"merchant_id": s.cfg.MerchantID, "authority": p.Authority, "amount": p.Amount * 10}
		var result struct {
			Data struct {
				Code   int    `json:"code"`
				RefID  string `json:"ref_id"`
				Amount int    `json:"amount"`
			} `json:"data"`
		}
		if err := s.postJSON(ctx, "https://api.zarinpal.com/pg/v4/payment/verify.json", payload, nil, &result); err != nil || (result.Data.Code != 100 && result.Data.Code != 101) {
			return "", fmt.Errorf("zarinpal re-verify failed for %d", p.ID)
		}
		if result.Data.Amount != p.Amount*10 {
			return "", fmt.Errorf("zarinpal amount mismatch for %d", p.ID)
		}
		return result.Data.RefID, nil
	}
}
