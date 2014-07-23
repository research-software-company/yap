package joint

import (
	"chukuparser/algorithm/perceptron"
	"chukuparser/algorithm/search"
	"chukuparser/algorithm/transition"
	transitionmodel "chukuparser/algorithm/transition/model"
	"chukuparser/nlp/format/conll"
	"chukuparser/nlp/format/lattice"
	"chukuparser/nlp/format/segmentation"
	"chukuparser/nlp/parser/dependency"
	. "chukuparser/nlp/parser/dependency/transition"
	"chukuparser/nlp/parser/dependency/transition/morph"
	"chukuparser/nlp/parser/disambig"
	"chukuparser/nlp/parser/joint"
	nlp "chukuparser/nlp/types"
	"chukuparser/util"
	"chukuparser/util/conf"

	// "encoding/gob"
	"fmt"
	"log"
	"os"
	"runtime"
	"sort"
	"strings"

	"github.com/gonuts/commander"
	"github.com/gonuts/flag"
)

var (
	allOut bool = true

	Iterations, BeamSize int
	ConcurrentBeam       bool
	NumFeatures          int

	// Global enumerations
	ERel, ETrans, EWord, EPOS, EWPOS, EMHost, EMSuffix *util.EnumSet
	EMorphProp                                         *util.EnumSet

	// Enumeration offsets of transitions
	SH, RE, PR, IDLE, LA, RA, MD transition.Transition

	tConll, tLatDis, tLatAmb string
	tSeg                     string
	input                    string
	inputGold                string
	outLat, outSeg           string
	modelFile                string
	featuresFile             string
	labelsFile               string
	paramFuncName            string

	REQUIRED_FLAGS []string = []string{"it", "tc", "td", "tl", "in", "oc", "os", "ots", "f", "l"}
)

// An approximation of the number of different MD-X:Y:Z transitions
// Pre-allocating the enumeration saves frequent reallocation during training and parsing
const (
	APPROX_MORPH_TRANSITIONS        = 100
	APPROX_WORDS, APPROX_POS        = 100, 100
	WORDS_POS_FACTOR                = 5
	APPROX_MHOSTS, APPROX_MSUFFIXES = 128, 16
)

func SetupRelationEnum(labels []string) {
	if ERel != nil {
		return
	}
	ERel = util.NewEnumSet(len(labels) + 1)
	ERel.Add(nlp.DepRel(nlp.ROOT_LABEL))
	for _, label := range labels {
		ERel.Add(nlp.DepRel(label))
	}
	ERel.Frozen = true
}

func SetupMorphTransEnum(relations []string) {
	ETrans = util.NewEnumSet((len(relations)+1)*2 + 2 + APPROX_MORPH_TRANSITIONS)
	_, _ = ETrans.Add("NO") // dummy for 0 action
	iSH, _ := ETrans.Add("SH")
	iRE, _ := ETrans.Add("RE")
	_, _ = ETrans.Add("AL") // dummy action transition for zpar equivalence
	_, _ = ETrans.Add("AR") // dummy action transition for zpar equivalence
	iPR, _ := ETrans.Add("PR")
	// iIDLE, _ := ETrans.Add("IDLE")
	SH = transition.Transition(iSH)
	RE = transition.Transition(iRE)
	PR = transition.Transition(iPR)
	// IDLE = transition.Transition(iIDLE)
	// LA = IDLE + 1
	LA = PR + 1
	ETrans.Add("LA-" + string(nlp.ROOT_LABEL))
	for _, transition := range relations {
		ETrans.Add("LA-" + string(transition))
	}
	RA = transition.Transition(ETrans.Len())
	ETrans.Add("RA-" + string(nlp.ROOT_LABEL))
	for _, transition := range relations {
		ETrans.Add("RA-" + string(transition))
	}
	log.Println("ETrans Len is", ETrans.Len())
	MD = transition.Transition(ETrans.Len())
}

func SetupEnum(relations []string) {
	SetupRelationEnum(relations)
	SetupMorphTransEnum(relations)
	EWord, EPOS, EWPOS = util.NewEnumSet(APPROX_WORDS), util.NewEnumSet(APPROX_POS), util.NewEnumSet(APPROX_WORDS*5)
	EMHost, EMSuffix = util.NewEnumSet(APPROX_MHOSTS), util.NewEnumSet(APPROX_MSUFFIXES)
	EMorphProp = util.NewEnumSet(130) // random guess of number of possible values

	// adding empty string as an element in the morph enum sets so that '0' default values
	// map to empty morphs
	EMHost.Add("")
	EMSuffix.Add("")
}

func SetupExtractor(setup *transition.FeatureSetup) *transition.GenericExtractor {
	extractor := &transition.GenericExtractor{
		EFeatures:  util.NewEnumSet(setup.NumFeatures()),
		Concurrent: false,
		EWord:      EWord,
		EPOS:       EPOS,
		EWPOS:      EWPOS,
		EMHost:     EMHost,
		EMSuffix:   EMSuffix,
		// Log:        true,
	}
	extractor.Init()
	extractor.LoadFeatureSetup(setup)
	// for _, feature := range features {
	// 	featurePair := strings.Split(feature, ",")
	// 	if err := extractor.LoadFeature(featurePair[0], featurePair[1]); err != nil {
	// 		log.Fatalln("Failed to load feature", err.Error())
	// 	}
	// }
	NumFeatures = setup.NumFeatures()
	return extractor
}

func TrainingSequences(trainingSet []interface{}, transitionSystem transition.TransitionSystem, extractor perceptron.FeatureExtractor) []perceptron.DecodedInstance {
	instances := make([]perceptron.DecodedInstance, 0, len(trainingSet))

	for _, config := range trainingSet {
		sent := config.(*joint.JointConfig)

		decoded := &perceptron.Decoded{sent, sent}
		instances = append(instances, decoded)
	}
	return instances
}

func Train(trainingSet []perceptron.DecodedInstance, Iterations, BeamSize int, filename string, model perceptron.Model, transitionSystem transition.TransitionSystem, extractor perceptron.FeatureExtractor) *perceptron.LinearPerceptron {
	conf := &joint.JointConfig{}

	beam := &search.Beam{
		TransFunc:            transitionSystem,
		FeatExtractor:        extractor,
		Base:                 conf,
		Size:                 BeamSize,
		ConcurrentExec:       ConcurrentBeam,
		Transitions:          ETrans,
		EstimatedTransitions: 1000,
	}

	deterministic := &search.Deterministic{
		TransFunc:          transitionSystem,
		FeatExtractor:      extractor,
		ReturnModelValue:   false,
		ReturnSequence:     true,
		ShowConsiderations: false,
		Base:               conf,
		NoRecover:          false,
	}

	// varbeam := &VarBeam{beam}
	decoder := perceptron.EarlyUpdateInstanceDecoder(beam)
	golddec := perceptron.InstanceDecoder(deterministic)
	updater := new(transitionmodel.AveragedModelStrategy)

	perceptron := &perceptron.LinearPerceptron{
		Decoder:     decoder,
		GoldDecoder: golddec,
		Updater:     updater,
		Tempfile:    filename,
		TempLines:   1000}

	perceptron.Iterations = Iterations
	perceptron.Init(model)
	// perceptron.TempLoad("model.b64.i1")
	perceptron.Log = true

	perceptron.Train(trainingSet)
	log.Println("TRAIN Total Time:", beam.DurTotal)

	return perceptron
}

func Parse(sents []nlp.LatticeSentence, BeamSize int, model transitionmodel.Interface, transitionSystem transition.TransitionSystem, extractor perceptron.FeatureExtractor) []nlp.Mappings {
	conf := &joint.JointConfig{}

	beam := search.Beam{
		TransFunc:            transitionSystem,
		FeatExtractor:        extractor,
		Base:                 conf,
		Size:                 BeamSize,
		Model:                model,
		ConcurrentExec:       ConcurrentBeam,
		ShortTempAgenda:      true,
		Transitions:          ETrans,
		EstimatedTransitions: 1000,
	}

	// varbeam := &VarBeam{beam}

	mdSents := make([]nlp.Mappings, len(sents))
	for i, sent := range sents {
		// if i%100 == 0 {
		runtime.GC()
		for _, lat := range sent {
			lat.BridgeMissingMorphemes()
		}
		log.Println("Parsing sent", i)
		// }
		mapped, _ := beam.Parse(sent)
		mdSents[i] = mapped.(*joint.JointConfig).Mappings
	}
	log.Println("PARSE Total Time:", beam.DurTotal)

	return mdSents
}

func Parse2(sents []nlp.LatticeSentence, BeamSize int, model dependency.TransitionParameterModel, transitionSystem transition.TransitionSystem, extractor perceptron.FeatureExtractor) []nlp.MorphDependencyGraph {
	conf := &morph.MorphConfiguration{
		SimpleConfiguration: SimpleConfiguration{
			EWord:  EWord,
			EPOS:   EPOS,
			EWPOS:  EWPOS,
			ERel:   ERel,
			ETrans: ETrans,
		},
	}

	beam := search.Beam{
		TransFunc:            transitionSystem,
		FeatExtractor:        extractor,
		Base:                 conf,
		Size:                 BeamSize,
		Model:                model,
		ConcurrentExec:       ConcurrentBeam,
		ShortTempAgenda:      true,
		Transitions:          ETrans,
		EstimatedTransitions: 1000,
	}

	// varbeam := &VarBeam{beam}

	parsedGraphs := make([]nlp.MorphDependencyGraph, len(sents))
	for i, sent := range sents {
		// if i%100 == 0 {
		runtime.GC()
		log.Println("Parsing sent", i)
		// }
		graph, _ := beam.Parse(sent)
		labeled := graph.(nlp.MorphDependencyGraph)
		parsedGraphs[i] = labeled
	}
	log.Println("PARSE Total Time:", beam.DurTotal)

	return parsedGraphs
}

// check this vs morph.CombineToGoldMorph
func CombineToGoldMorph(goldLat, ambLat nlp.LatticeSentence) (*joint.JointConfig, bool) {
	var addedMissingSpellout bool
	// generate graph

	// generate morph. disambiguation (= mapping) and nodes
	mappings := make([]*nlp.Mapping, len(goldLat))
	for i, lat := range goldLat {
		lat.GenSpellouts()
		lat.GenToken()
		if len(lat.Spellouts) == 0 {
			continue
		}
		mapping := &nlp.Mapping{
			lat.Token,
			lat.Spellouts[0],
		}
		// if the gold spellout doesn't exist in the lattice, add it
		_, exists := ambLat[i].Spellouts.Find(mapping.Spellout)
		if !exists {
			ambLat[i].Spellouts = append(ambLat[i].Spellouts, mapping.Spellout)
			addedMissingSpellout = true
			ambLat[i].UnionPath(&lat)
		}
		ambLat[i].BridgeMissingMorphemes()

		mappings[i] = mapping
	}

	m := &joint.JointConfig{
		SimpleConfiguration: SimpleConfiguration{},
		MDConfig: disambig.MDConfig{
			Mappings: mappings,
			Lattices: ambLat,
		},
	}
	return m, addedMissingSpellout
}

func CombineTrainingInputs(graphs []nlp.LabeledDependencyGraph, goldLats, ambLats []nlp.LatticeSentence) ([]interface{}, int) {
	if len(graphs) != len(goldLats) || len(graphs) != len(ambLats) {
		panic(fmt.Sprintf("Got mismatched training slice inputs (graphs, gold lattices, ambiguous lattices):", len(graphs), len(goldLats), len(ambLats)))
	}
	morphGraphs := make([]interface{}, len(graphs))
	var (
		numLatticeNoGold int
		noGold           bool
	)
	prefix := log.Prefix()
	for i, goldGraph := range graphs {
		goldLat := goldLats[i]
		ambLat := ambLats[i]
		log.SetPrefix(fmt.Sprintf("%v graph# %v ", prefix, i))
		morphGraphs[i], noGold = morph.CombineToGoldMorph(goldGraph, goldLat, ambLat)
		if noGold {
			numLatticeNoGold++
		}
	}
	log.SetPrefix(prefix)
	return morphGraphs, numLatticeNoGold
}

func CombineToGoldMorphs(goldLats, ambLats []nlp.LatticeSentence) ([]interface{}, int) {
	if len(goldLats) != len(ambLats) {
		panic(fmt.Sprintf("Got mismatched training slice inputs (gold lattices, ambiguous lattices):", len(goldLats), len(ambLats)))
	}
	morphGraphs := make([]interface{}, len(goldLats))
	var (
		numLatticeNoGold int
		noGold           bool
	)
	prefix := log.Prefix()
	for i, goldLat := range goldLats {
		ambLat := ambLats[i]
		log.SetPrefix(fmt.Sprintf("%v lattice# %v ", prefix, i))
		morphGraphs[i], noGold = CombineToGoldMorph(goldLat, ambLat)
		if noGold {
			numLatticeNoGold++
		}
	}
	log.SetPrefix(prefix)
	return morphGraphs, numLatticeNoGold
}

func VerifyExists(filename string) bool {
	_, err := os.Stat(filename)
	if err != nil {
		log.Println("Error accessing file", filename)
		log.Println(err.Error())
		return false
	}
	return true
}

func VerifyFlags(cmd *commander.Command) {
	for _, flag := range REQUIRED_FLAGS {
		f := cmd.Flag.Lookup(flag)
		if f == nil || f.Value == nil || f.Value.String() == "" {
			log.Printf("Required flag %s not set", flag)
			cmd.Usage()
			os.Exit(1)
		}
	}
}

func ConfigOut(outModelFile string, b search.Interface, t transition.TransitionSystem) {
	log.Println("Configuration")
	log.Printf("Beam:             \t%s", b.Name())
	log.Printf("Transition System:\t%s", t.Name())
	log.Printf("Iterations:\t\t%d", Iterations)
	log.Printf("Beam Size:\t\t%d", BeamSize)
	log.Printf("Beam Concurrent:\t%v", ConcurrentBeam)
	log.Printf("Parameter Func:\t%v", paramFuncName)
	// log.Printf("Model file:\t\t%s", outModelFile)

	log.Println()
	log.Printf("Features File:\t%s", featuresFile)
	if !VerifyExists(featuresFile) {
		os.Exit(1)
	}
	log.Printf("Labels File:\t\t%s", labelsFile)
	if !VerifyExists(labelsFile) {
		os.Exit(1)
	}
	log.Println()
	log.Println("Data")
	log.Printf("Train file (conll):\t\t\t%s", tConll)
	if !VerifyExists(tConll) {
		return
	}
	log.Printf("Train file (disamb. lattice):\t%s", tLatDis)
	if !VerifyExists(tLatDis) {
		return
	}
	log.Printf("Train file (ambig.  lattice):\t%s", tLatAmb)
	if !VerifyExists(tLatAmb) {
		return
	}
	log.Printf("Test file  (ambig.  lattice):\t%s", input)
	if !VerifyExists(input) {
		return
	}
	if len(inputGold) > 0 {
		log.Printf("Test file  (disambig.  lattice):\t%s", inputGold)
		if !VerifyExists(inputGold) {
			return
		}
	}
	log.Printf("Out (disamb.) file:\t\t\t%s", outLat)
	log.Printf("Out (segmt.) file:\t\t\t%s", outSeg)
	log.Printf("Out Train (segmt.) file:\t\t%s", tSeg)
}

func JointTrainAndParse(cmd *commander.Command, args []string) {
	// *** SETUP ***
	paramFunc, exists := disambig.MDParams[paramFuncName]
	if !exists {
		log.Fatalln("Param Func", paramFuncName, "does not exist")
	}

	mdTrans := &disambig.MDTrans{
		ParamFunc: paramFunc,
	}

	arcSystem := &ArcStandard{
		SHIFT:       SH,
		LEFT:        LA,
		RIGHT:       RA,
		Relations:   ERel,
		Transitions: ETrans,
	}

	arcSystem.AddDefaultOracle()

	jointTrans := &joint.JointTrans{
		MDTrans: mdTrans,
		ArcSys:  arcSystem,
	}
	transitionSystem := transition.TransitionSystem(jointTrans)

	VerifyFlags(cmd)
	// RegisterTypes()

	outModelFile := fmt.Sprintf("%s.b%d.i%d", modelFile, BeamSize, Iterations)

	ConfigOut(outModelFile, &search.Beam{}, transitionSystem)

	relations, err := conf.ReadFile(labelsFile)
	if err != nil {
		log.Println("Failed reading dependency labels configuration file:", labelsFile)
		log.Fatalln(err)
	}
	if allOut {
		log.Println()
		// start processing - setup enumerations
		log.Println("Setup enumerations")
	}
	SetupEnum(relations.Values)
	mdTrans.Transitions = ETrans
	mdTrans.AddDefaultOracle()

	if allOut {
		log.Println()
		log.Println("Loading features")
	}
	featureSetup, err := transition.LoadFeatureConfFile(featuresFile)
	if err != nil {
		log.Println("Failed reading feature configuration file:", featuresFile)
		log.Fatalln(err)
	}
	extractor := SetupExtractor(featureSetup)

	// *** TRAINING ***

	if allOut {
		log.Println("Generating Gold Sequences For Training")
		log.Println("Conll:\tReading training conll sentences from", tConll)
	}
	s, e := conll.ReadFile(tConll)
	if e != nil {
		log.Println(e)
		return
	}
	if allOut {
		log.Println("Conll:\tRead", len(s), "sentences")
		log.Println("Conll:\tConverting from conll to internal structure")
	}
	goldConll := conll.Conll2GraphCorpus(s, EWord, EPOS, EWPOS, ERel, nil, nil)

	if allOut {
		log.Println("Dis. Lat.:\tReading training disambiguated lattices from", tLatDis)
	}
	lDis, lDisE := lattice.ReadFile(tLatDis)
	if lDisE != nil {
		log.Println(lDisE)
		return
	}
	if allOut {
		log.Println("Dis. Lat.:\tRead", len(lDis), "disambiguated lattices")
		log.Println("Dis. Lat.:\tConverting lattice format to internal structure")
	}
	goldDisLat := lattice.Lattice2SentenceCorpus(lDis, EWord, EPOS, EWPOS, EMorphProp)

	if allOut {
		log.Println("Amb. Lat:\tReading ambiguous lattices from", tLatAmb)
	}
	lAmb, lAmbE := lattice.ReadFile(tLatAmb)
	if lAmbE != nil {
		log.Println(lAmbE)
		return
	}
	if allOut {
		log.Println("Amb. Lat:\tRead", len(lAmb), "ambiguous lattices")
		log.Println("Amb. Lat:\tConverting lattice format to internal structure")
	}
	goldAmbLat := lattice.Lattice2SentenceCorpus(lAmb, EWord, EPOS, EWPOS, EMorphProp)
	if allOut {
		log.Println("Combining train files into gold morph graphs with original lattices")
	}
	combined, missingGold := CombineTrainingInputs(goldConll, goldDisLat, goldAmbLat)

	if allOut {
		log.Println("Combined", len(combined), "graphs, with", missingGold, "missing at least one gold path in lattice")

		log.Println()

	}

	_ = &joint.JointConfig{}

	if allOut {
		log.Println()

		log.Println("Parsing with gold to get training sequences")
	}
	// const NUM_SENTS = 20
	// combined = combined[:NUM_SENTS]
	goldSequences := TrainingSequences(combined, transitionSystem, extractor)
	if allOut {
		log.Println("Generated", len(goldSequences), "training sequences")
		log.Println()
		// util.LogMemory()
		log.Println("Training", Iterations, "iteration(s)")
	}
	formatters := make([]util.Format, len(extractor.FeatureTemplates))
	for i, formatter := range extractor.FeatureTemplates {
		formatters[i] = formatter
	}
	model := transitionmodel.NewAvgMatrixSparse(NumFeatures, formatters, true)
	_ = Train(goldSequences, Iterations, BeamSize, modelFile, model, transitionSystem, extractor)
	if allOut {
		log.Println("Done Training")
		// util.LogMemory()
		log.Println()
		// log.Println("Writing final model to", outModelFile)
		// WriteModel(model, outModelFile)
		// log.Println()
		log.Print("Parsing test")

		log.Println("Reading ambiguous lattices from", input)
	}

	// *** PARSING ***

	lAmb, lAmbE = lattice.ReadFile(input)
	if lAmbE != nil {
		log.Println(lAmbE)
		return
	}
	// lAmb = lAmb[:NUM_SENTS]
	if allOut {
		log.Println("Read", len(lAmb), "ambiguous lattices from", input)
		log.Println("Converting lattice format to internal structure")
	}
	predAmbLat := lattice.Lattice2SentenceCorpus(lAmb, EWord, EPOS, EWPOS, EMorphProp)

	if len(inputGold) > 0 {
		log.Println("Reading test disambiguated lattice (for test ambiguous infusion)")
		lDis, lDisE = lattice.ReadFile(inputGold)
		if lDisE != nil {
			log.Println(lDisE)
			return
		}
		if allOut {
			log.Println("Test Gold Dis. Lat.:\tRead", len(lDis), "disambiguated lattices")
			log.Println("Test Gold Dis. Lat.:\tConverting lattice format to internal structure")
		}

		predDisLat := lattice.Lattice2SentenceCorpus(lDis, EWord, EPOS, EWPOS, EMorphProp)

		if allOut {
			log.Println("Infusing test's gold disambiguation into ambiguous lattice")
		}

		_, missingGold = CombineToGoldMorphs(predDisLat, predAmbLat)

		if allOut {
			log.Println("Combined", len(combined), "graphs, with", missingGold, "missing at least one gold path in lattice")

			log.Println()
		}
	}

	parsedGraphs := Parse(predAmbLat, BeamSize, model, transitionSystem, extractor)

	if allOut {
		log.Println("Converting", len(parsedGraphs), "to conll")
	}
	graphAsConll := conll.MorphGraph2ConllCorpus(parsedGraphs)
	if allOut {
		log.Println("Writing to output file")
	}
	// conll.WriteFile(outLat, graphAsConll)
	// if allOut {
	// 	log.Println("Wrote", len(graphAsConll), "in conll format to", outLat)

	// 	log.Println("Writing to segmentation file")
	// }
	segmentation.WriteFile(outSeg, parsedGraphs)
	if allOut {
		log.Println("Wrote", len(parsedGraphs), "in segmentation format to", outSeg)

		log.Println("Writing to gold segmentation file")
	}
	segmentation.WriteFile(tSeg, ToMorphGraphs(combined))
	if allOut {
		log.Println("Wrote", len(combined), "in segmentation format to", tSeg)
	}
}

func ToMorphGraphs(graphs []*morph.BasicMorphGraph) []nlp.MorphDependencyGraph {
	morphs := make([]nlp.MorphDependencyGraph, len(graphs))
	for i, g := range graphs {
		morphs[i] = nlp.MorphDependencyGraph(g)
	}
	return morphs
}

func JointCmd() *commander.Command {
	cmd := &commander.Command{
		Run:       JointTrainAndParse,
		UsageLine: "joint <file options> [arguments]",
		Short:     "runs joint morpho-syntactic training and parsing",
		Long: `
runs morpho-syntactic training and parsing

	$ ./chukuparser joint -tc <conll> -td <train disamb. lat> -tl <train amb. lat> -in <input lat> -oc <out lat> -os <out seg> -ots <out train seg> [options]

`,
		Flag: *flag.NewFlagSet("joint", flag.ExitOnError),
	}
	cmd.Flag.BoolVar(&ConcurrentBeam, "bconc", false, "Concurrent Beam")
	cmd.Flag.IntVar(&Iterations, "it", 1, "Number of Perceptron Iterations")
	cmd.Flag.IntVar(&BeamSize, "b", 4, "Beam Size")
	cmd.Flag.StringVar(&modelFile, "m", "model", "Prefix for model file ({m}.b{b}.i{it}.model)")

	cmd.Flag.StringVar(&tConll, "tc", "", "Training Conll File")
	cmd.Flag.StringVar(&tLatDis, "td", "", "Training Disambiguated Lattices File")
	cmd.Flag.StringVar(&tLatAmb, "tl", "", "Training Ambiguous Lattices File")
	cmd.Flag.StringVar(&input, "in", "", "Test Ambiguous Lattices File")
	cmd.Flag.StringVar(&inputGold, "ing", "", "Optional - Gold Test Lattices File (for infusion into test ambiguous)")
	cmd.Flag.StringVar(&outLat, "oc", "", "Output Conll File")
	cmd.Flag.StringVar(&outSeg, "os", "", "Output Segmentation File")
	cmd.Flag.StringVar(&tSeg, "ots", "", "Output Training Segmentation File")
	cmd.Flag.StringVar(&featuresFile, "f", "", "Features Configuration File")
	cmd.Flag.StringVar(&labelsFile, "l", "", "Dependency Labels Configuration File")

	paramFuncStrs := make([]string, 0, len(disambig.MDParams))
	for k, _ := range disambig.MDParams {
		paramFuncStrs = append(paramFuncStrs, k)
	}
	sort.Strings(paramFuncStrs)
	cmd.Flag.StringVar(&paramFuncName, "p", "POS", "Param Func types: ["+strings.Join(paramFuncStrs, ", ")+"]")

	return cmd
}
