package taskgraph_op

// This file lists common stopping criteria for optimization.

// Criterion as Frobenos norm is smaller than a given tolerance.
type GradNormTolCriteria struct {
	tolerance float32
}

func (c *GradNormTolCriteria) Done(param Parameter, value float32, gradient Parameter) bool {
	var gradNorm float32 = 0.0
	for iter := gradient.IndexIterator(); iter.Next(); {
		v := gradient.Get(iter.Index())
		gradNorm += v * v
	}
	return gradNorm < c.tolerance*c.tolerance
}