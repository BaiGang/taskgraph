package bwmf

import (
	"github.com/taskgraph/taskgraph/op"
	"math"
	"sync"
)

// `KLDivLoss` is a `Function` that evaluates Kullback-Leibler Divergence and the corresponding gradient at the given `Parameter`.
//
//  XXX(baigang): matrix layout
//    W is vectorized by the mapping W[ I, J ] = W_para[ I * k + J ]
//    H is vectorized by the mapping H[ I, J ] = H_para[ I * k + J ]
//  So actually H is H^T, but it saves code by using identical routine when alternatively optimize over H and W.
//
type KLDivLoss struct {
	V       []map[int]float32 // write once, read concurrently multiple times
	WH      [][]float32       // temporary storage for the intermediate result W*H
	W       taskgraph_op.Parameter
	m, n, k int // dimensions
	smooth  float64
}

// This function evaluates the Kullback-Leibler Divergence given $\mathbf{V} the matrix to fact and $\mathbf{W}$ the fixed factor.
//  The generalized KL div is:
//
//    $$ D_{KL} = \Sum_{ij} ( V_{ij} log \frac{V_{ij}}{(WH)_{ij}} - V_{ij} + (WH_{ij} )
//
//  After removing the redundant constant factor and adding the smooth factor, it becomes:
//
//    $$ L_{kl} = \Sum{ij} ( -V_{ij} log((WH)_{ij} + smooth) + (WH)_{ij} )
//
//  The gradient is:
//
//  $$ \divsymb \frac{D_{KL}}{H} = -W*Z^T + W^T*\bar{Z} $$
//  , where $Z_{ij} = \frac{V_{ij}}{(WH)_{ij}}$
//
//  This implementation consists of two pass of visiting the full matrix, each of
//  which goes parallel. One pass is for evaluating W*H and accumulate kl-divergence
//  value and the other is for evalutating the matrix gradient of kl-div.
//
func (l *KLDivLoss) Evaluate(param taskgraph_op.Parameter, gradient taskgraph_op.Parameter) float32 {
	H := param
	accum := make(chan float32, 8)
	for i := 0; i < l.m; i++ {
		for j := 0; j < l.n; j++ {
			l.WH[i][j] = 0.0
			go func(i, j int) {
				for k := 0; k < l.k; k++ {
					l.WH[i][j] += l.W.Get(i*l.k+k) * H.Get(j*l.k+k)
				}

				// evaluate element-wise KL-divergence
				v, ok := l.V[i][j]
				wh := l.WH[i][j]
				if ok {
					accum <- -v*float32(math.Log(l.smooth+float64(wh))) + wh
				} else {
					accum <- wh
				}
			}(i, j)
		}
	}

	var value float32
	for c := 0; c < l.m*l.n; c++ {
		value += <-accum
	}

	// now, another pass for grad calculation
	var wg sync.WaitGroup
	for i := 0; i < l.k; i++ {
		for j := 0; j < l.n; j++ {
			wg.Add(1)
			grad_index := j*l.k + i
			gradient.Set(grad_index, 0.0)
			go func(grad_index, i, j int) {
				defer wg.Done()
				for k := 0; k < l.m; k++ {
					w_index := k*l.k + i
					grad_val := l.W.Get(w_index) * (l.WH[k][j] - l.V[k][j]) / l.WH[k][j]
					gradient.Add(grad_index, grad_val)
				}
			}(grad_index, i, j)
		}
	}
	wg.Wait()

	return value
}