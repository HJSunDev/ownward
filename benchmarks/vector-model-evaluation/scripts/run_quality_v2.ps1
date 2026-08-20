$ErrorActionPreference = 'Stop'

$evaluationRoot = 'E:\Dev\ownward\.tmp\vector-model-evaluation'
$python = Join-Path $evaluationRoot 'venv\Scripts\python.exe'
$evaluator = Join-Path $evaluationRoot 'scripts\evaluate_ownward.py'
$config = Join-Path $evaluationRoot 'state\frozen-config-v2.json'
$dataset = Join-Path $evaluationRoot 'data\ownward-v2'
$results = Join-Path $evaluationRoot 'results\quality-v2'

$env:TEMP = Join-Path $evaluationRoot 'temp'
$env:TMP = $env:TEMP
$env:HF_HOME = Join-Path $evaluationRoot 'hf-cache'
$env:PYTHONPYCACHEPREFIX = Join-Path $evaluationRoot 'pycache'

$models = @(
    @{
        Key = 'bge_m3'
        Directory = 'bge-m3-int8'
    },
    @{
        Key = 'embeddinggemma_300m'
        Directory = 'embeddinggemma-int8'
    },
    @{
        Key = 'qwen3_embedding_0_6b'
        Directory = 'qwen3-int8'
    }
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
        throw "质量测试失败：$($model.Key)，退出码 $LASTEXITCODE"
    }
}
