package server

import (
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/JoaoAnunciacaoDev/PBL---VaiJunto-Concorrencia-E-Conectividade/internal/models"
	"github.com/google/uuid"
)

// SaveRating armazena uma nova avaliação ou registro de ocorrência.
func (r *Repository) SaveRating(rating *models.Rating) error {
	if rating == nil {
		return errors.New("avaliação inválida")
	}
	if rating.AuthorID == uuid.Nil || rating.TargetID == uuid.Nil {
		return errors.New("autor e alvo da avaliação são obrigatórios")
	}
	if rating.ID == uuid.Nil {
		rating.ID = uuid.New()
	}
	if rating.CreatedAt.IsZero() {
		rating.CreatedAt = time.Now()
	}

	r.ratingsMu.Lock()
	defer r.ratingsMu.Unlock()

	r.ratings[rating.ID] = cloneRating(rating)

	if err := r.saveStateLocked(); err != nil {
		delete(r.ratings, rating.ID)
		return fmt.Errorf("salvar avaliação: %w", err)
	}

	return nil
}

// HasUserRated verifica se o autor já enviou uma avaliação para aquele alvo na mesma carona.
func (r *Repository) HasUserRated(authorID, rideID, targetID uuid.UUID) bool {
	r.ratingsMu.RLock()
	defer r.ratingsMu.RUnlock()

	for _, item := range r.ratings {
		if item.AuthorID == authorID && item.RideID == rideID && item.TargetID == targetID {
			return true
		}
	}
	return false
}

// GetReputationByUserID consolida a reputação e histórico completo de um usuário.
func (r *Repository) GetReputationByUserID(userID uuid.UUID) (*models.UserReputation, error) {
	if userID == uuid.Nil {
		return nil, errors.New("identificador de usuário inválido")
	}

	r.usersMu.RLock()
	userName := ""
	for _, u := range r.users {
		if u.ID == userID {
			userName = u.Name
			break
		}
	}
	r.usersMu.RUnlock()

	r.ratingsMu.RLock()
	defer r.ratingsMu.RUnlock()

	var userRatings []models.Rating
	totalScore := 0
	countRatings := 0
	noShowCount := 0
	lateCancelledCount := 0

	for _, rating := range r.ratings {
		if rating.TargetID == userID {
			userRatings = append(userRatings, *cloneRating(rating))

			switch rating.Type {
			case models.RatingTypePassengerToDriver, models.RatingTypeDriverToPassenger:
				if rating.Score >= 1 && rating.Score <= 5 {
					totalScore += rating.Score
					countRatings++
				}
			case models.RatingTypeNoShow:
				noShowCount++
			case models.RatingTypeLateCancellation:
				lateCancelledCount++
			}
		}
	}

	sort.Slice(userRatings, func(i, j int) bool {
		return userRatings[i].CreatedAt.After(userRatings[j].CreatedAt)
	})

	avgScore := 0.0
	if countRatings > 0 {
		avgScore = float64(totalScore) / float64(countRatings)
	}

	return &models.UserReputation{
		UserID:             userID,
		UserName:           userName,
		AverageScore:       avgScore,
		TotalRatings:       countRatings,
		NoShowCount:        noShowCount,
		LateCancelledCount: lateCancelledCount,
		Ratings:            userRatings,
	}, nil
}
