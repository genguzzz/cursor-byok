package modeladapter

import (
	"bytes"
	"fmt"
	"image"
	"image/jpeg"
	_ "image/png"
	"os"
	"strings"
)

// ReadToolImageReplayLimit 与 CodeBuddy CLI 压缩后 JPEG（约 266KB）对齐，留一定余量。
const ReadToolImageReplayLimit = 384 * 1024

const readToolImageMaxSide = 2048

// anthropicVisionMinSide：过窄/过矮截图（如 714×82）直接送 tclaude 时模型常只读到尺寸文案；
// 放大到最小边后再送，接近 CLI 常见识图尺寸。
const anthropicVisionMinSide = 256

// CompressReadImageForReplay 把超限图片压缩为 JPEG，模拟 CodeBuddy「38MB → ~267KB」行为。
// 若本身已是可识别图片且不超过 limit，则原样返回。
func CompressReadImageForReplay(path string, payload []byte, limit int) ([]byte, error) {
	if len(payload) == 0 {
		return nil, fmt.Errorf("empty image payload")
	}
	if limit <= 0 {
		limit = ReadToolImageReplayLimit
	}
	if isImagePayload(payload) && len(payload) <= limit {
		return payload, nil
	}
	img, _, err := image.Decode(bytes.NewReader(payload))
	if err != nil {
		// 非可解码图片：若已超限，交给调用方截断逻辑。
		if len(payload) <= limit {
			return payload, nil
		}
		return nil, fmt.Errorf("decode image for compression: %w", err)
	}
	scaled := scaleImageMaxSide(img, readToolImageMaxSide)
	for _, quality := range []int{80, 70, 60, 50, 40, 30, 20} {
		var buf bytes.Buffer
		if err := jpeg.Encode(&buf, scaled, &jpeg.Options{Quality: quality}); err != nil {
			return nil, fmt.Errorf("jpeg encode q=%d: %w", quality, err)
		}
		if buf.Len() <= limit {
			return buf.Bytes(), nil
		}
	}
	// 仍超限则继续缩小边长。
	for side := 1600; side >= 640; side -= 320 {
		smaller := scaleImageMaxSide(img, side)
		var buf bytes.Buffer
		if err := jpeg.Encode(&buf, smaller, &jpeg.Options{Quality: 40}); err != nil {
			return nil, err
		}
		if buf.Len() <= limit {
			return buf.Bytes(), nil
		}
	}
	return nil, fmt.Errorf("unable to compress image under %d bytes (path=%s)", limit, strings.TrimSpace(path))
}

// LoadAndCompressReadImageFile 读取本地图片文件并压缩到 replay 上限，供真实链路测试使用。
func LoadAndCompressReadImageFile(path string, limit int) ([]byte, error) {
	payload, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return CompressReadImageForReplay(path, payload, limit)
}

func scaleImageMaxSide(src image.Image, maxSide int) image.Image {
	if maxSide <= 0 {
		return src
	}
	bounds := src.Bounds()
	w, h := bounds.Dx(), bounds.Dy()
	if w <= 0 || h <= 0 {
		return src
	}
	if w <= maxSide && h <= maxSide {
		return src
	}
	scale := float64(maxSide) / float64(w)
	if h > w {
		scale = float64(maxSide) / float64(h)
	}
	return scaleImageToSize(src, int(float64(w)*scale), int(float64(h)*scale))
}

func scaleImageMinSide(src image.Image, minSide int) image.Image {
	if minSide <= 0 {
		return src
	}
	bounds := src.Bounds()
	w, h := bounds.Dx(), bounds.Dy()
	if w <= 0 || h <= 0 {
		return src
	}
	if w >= minSide && h >= minSide {
		return src
	}
	scale := float64(minSide) / float64(w)
	if float64(minSide)/float64(h) > scale {
		scale = float64(minSide) / float64(h)
	}
	return scaleImageToSize(src, int(float64(w)*scale+0.5), int(float64(h)*scale+0.5))
}

func scaleImageToSize(src image.Image, nw, nh int) image.Image {
	if nw < 1 {
		nw = 1
	}
	if nh < 1 {
		nh = 1
	}
	bounds := src.Bounds()
	w, h := bounds.Dx(), bounds.Dy()
	if w == nw && h == nh {
		return src
	}
	dst := image.NewRGBA(image.Rect(0, 0, nw, nh))
	for y := 0; y < nh; y++ {
		sy := bounds.Min.Y + y*h/nh
		for x := 0; x < nw; x++ {
			sx := bounds.Min.X + x*w/nw
			dst.Set(x, y, src.At(sx, sy))
		}
	}
	return dst
}
