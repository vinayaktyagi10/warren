package forest

import "github.com/vinayaktyagi10/warren/internal/detect"

// InputSet is the feature space the forest measures anomaly in.
//
// It is deliberately not whichever set the ranker was asked for. The forest's
// input must not contain its own output, and it must not contain the registry
// share either: a group whose accounts are on a suspect list is unusual by
// construction, so including it would let the forest rediscover the list and
// report it as novelty.
const InputSet = detect.FeatureSetTemporal

// Annotate fits a forest on the fitting candidates and scores every candidate
// with it.
//
// Fitting on the training split only is not a formality even though the forest
// takes no labels. It still learns the shape of the candidate population, and
// letting it see the held-out period is letting the model's notion of "ordinary"
// be informed by the very window it is about to be judged on.
func Annotate(train, all []detect.Candidate, opts Opts) *Forest {
	f := Train(detect.VectorsFor(train, InputSet), opts)
	for i := range all {
		all[i].Features.Anomaly = f.Score(all[i].Features.VectorFor(InputSet))
	}
	return f
}
