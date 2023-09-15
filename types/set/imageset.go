package set

import (
	"github.com/terrails/yacu/types/image"
)

type ImageSet struct {
	Items map[string]*image.ImageData
}

func NewImageSet() *ImageSet {
	s := &ImageSet{
		Items: make(map[string]*image.ImageData),
	}
	return s
}

func (s *ImageSet) Add(img *image.ImageData) *ImageSet {
	if s.Items == nil {
		s.Items = make(map[string]*image.ImageData)
	}
	if _, ok := s.Items[img.ID]; !ok {
		s.Items[img.ID] = img
	}
	return s
}

func (s *ImageSet) Remove(img *image.ImageData) *ImageSet {
	delete(s.Items, img.ID)
	return s
}

func (s *ImageSet) Contains(img *image.ImageData) bool {
	_, ok := s.Items[img.ID]
	return ok
}

func (s *ImageSet) Size() int {
	return len(s.Items)
}

func (s *ImageSet) Clear() {
	// Clear the map instead of overwriting
	for k := range s.Items {
		delete(s.Items, k)
	}
}
