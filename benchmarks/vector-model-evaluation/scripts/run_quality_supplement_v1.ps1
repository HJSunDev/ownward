$ErrorActionPreference = 'Stop'

$evaluationRoot = 'E:\Dev\ownward\.tmp\vector-model-evaluation'
$python = Join-Path $evaluationRoot 'venv\Scripts\python.exe'
$evaluator = Join-Path $evaluationRoot 'scripts\evaluate_ownward.py'
$summarizer = Join-Path $evaluationRoot 'scripts\summarize_quality_supplement_v1.py'
$config = Join-Path $evaluationRoot 'state\quality-supplement-v1-config.json'
$dataset = Join-Path $evaluationRoot 'data\ownward-quality-supplement-v1'
$results = Join-Path $evaluationRoot 'results\quality-supplement-v1'

$env:TEMP = Join-Path $evaluationRoot 'temp'
$env:TMP = $env:TEMP
$env:HF_HOME = Join-Path $evaluationRoot 'hf-cache'
$env:PYTHONPYCACHEPREFIX = Join-Path $evaluationRoot 'pycache'

$models = @(
    @{ Key = 'bge_m3'; Directory = 'bge-m3-int8' },
    @{ Key = 'embeddinggemma_300m'; Directory = 'embeddinggemma-int8' },
    @{ Key = 'qwen3_embedding_0_6b'; Directory = 'qwen3-int8' }
)

foreach ($model in $models) {
    $modelDirectory = Join-Path (Join-Path $evaluationRoot 'models') $model.Directory
    $output = Join-Path $results $model.Key
    & $python $evaluator `
        --config $config `
        --model-key $model.Key `
        --variant deliverable `
        --model-dir $modelDirectory `
        --dataset $dataset `
        --output $output `
        --track formal
    if ($LASTEXITCODE -ne 0) {
        throw "补测失败：$($model.Key)，退出码 $LASTEXITCODE"
    }
}

& $python $summarizer
if ($LASTEXITCODE -ne 0) {
    throw "补测汇总失败，退出码 $LASTEXITCODE"
}
