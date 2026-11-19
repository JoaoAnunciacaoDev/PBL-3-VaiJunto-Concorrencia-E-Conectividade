package server

import (
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/JoaoAnunciacaoDev/PBL---VaiJunto-Concorrencia-E-Conectividade/internal/models"
	"github.com/google/uuid"
)

// SaveRating armazena de forma idempotente uma nova avaliação ou registro de ocorrência.
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

	r.writeMu.Lock()
	defer r.writeMu.Unlock()

	r.ratingsMu.Lock()
	defer r.ratingsMu.Unlock()

	if _, exists := r.ratings[rating.ID]; exists {
		return nil
	}

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
		if item.AuthorID == authorID && item.RideID == rideID && item.TargetID == targetID &&
			(item.Type == models.RatingTypePassengerToDriver || item.Type == models.RatingTypeDriverToPassenger) {
			return true
		}
	}
	return false
}

// HasRatingOfType verifica se já existe um registro de reputação específico para autor, carona, alvo e tipo.
func (r *Repository) HasRatingOfType(authorID, rideID, targetID uuid.UUID, ratingType models.RatingType) bool {
	r.ratingsMu.RLock()
	defer r.ratingsMu.RUnlock()

	for _, item := range r.ratings {
		if item.AuthorID == authorID && item.RideID == rideID && item.TargetID == targetID && item.Type == ratingType {
			return true
		}
	}
	return false
}

// HasNoShowForRide verifica se o passageiro possui ocorrência de ausência registrada na carona.
func (r *Repository) HasNoShowForRide(rideID, passengerID uuid.UUID) bool {
	r.ratingsMu.RLock()
	defer r.ratingsMu.RUnlock()

	for _, item := range r.ratings {
		if item.RideID == rideID && item.TargetID == passengerID && item.Type == models.RatingTypeNoShow {
			return true
		}
	}
	return false
}

// GetReputationByUserID consolida a reputação e histórico completo local de um usuário.
func (r *Repository) GetReputationByUserID(userID uuid.UUID) (*models.UserReputation, error) {
	if userID == uuid.Nil {
		return nil, errors.New("identificador de usuário inválido")
	}

	r.ratingsMu.RLock()
	var userRatings []models.Rating
	for _, rating := range r.ratings {
		if rating.TargetID == userID {
			userRatings = append(userRatings, *cloneRating(rating))
		}
	}
	r.ratingsMu.RUnlock()

	// Adquire usersMu depois ou separadamente (nunca invertendo ordem ratingsMu -> usersMu)
	r.usersMu.RLock()
	userName := ""
	if u, ok := r.userByIDLocked(userID); ok {
		userName = u.Name
	}
	r.usersMu.RUnlock()

	return aggregateUserReputation(userID, userName, userRatings), nil
}

func aggregateUserReputation(userID uuid.UUID, userName string, rawRatings []models.Rating) *models.UserReputation {
	seen := make(map[uuid.UUID]struct{}, len(rawRatings))
	unique := make([]models.Rating, 0, len(rawRatings))

	totalScore := 0
	countRatings := 0
	noShowCount := 0
	lateCancelledCount := 0
	complaintCount := 0
	driverCancelledCount := 0

	for _, rating := range rawRatings {
		if rating.ID != uuid.Nil {
			if _, exists := seen[rating.ID]; exists {
				continue
			}
			seen[rating.ID] = struct{}{}
		}
		unique = append(unique, rating)

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
		case models.RatingTypeComplaint:
			complaintCount++
		case models.RatingTypeDriverCancellation:
			driverCancelledCount++
		}
	}

	sort.Slice(unique, func(i, j int) bool {
		return unique[i].CreatedAt.After(unique[j].CreatedAt)
	})

	avgScore := 0.0
	if countRatings > 0 {
		avgScore = float64(totalScore) / float64(countRatings)
	}

	return &models.UserReputation{
		UserID:               userID,
		UserName:             userName,
		AverageScore:         avgScore,
		TotalRatings:         countRatings,
		NoShowCount:          noShowCount,
		LateCancelledCount:   lateCancelledCount,
		ComplaintCount:       complaintCount,
		DriverCancelledCount: driverCancelledCount,
		Ratings:              unique,
	}
}
