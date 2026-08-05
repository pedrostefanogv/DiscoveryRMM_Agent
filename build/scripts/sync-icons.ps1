[CmdletBinding()]
param(
    [string]$ProjectRoot = (Resolve-Path (Join-Path $PSScriptRoot "..\..")).Path
)

$ErrorActionPreference = "Stop"

Add-Type -AssemblyName System.Drawing

function Convert-PngToIco {
    param(
        [Parameter(Mandatory = $true)]
        [string]$SourcePng,
        [Parameter(Mandatory = $true)]
        [string]$TargetIco,
        [Parameter(Mandatory = $true)]
        [int[]]$IconSizes
    )

    $sourceStream = $null
    $sourceImage = $null

    try {
        $sourceStream = [System.IO.File]::OpenRead($SourcePng)
        $sourceImage = [System.Drawing.Image]::FromStream($sourceStream, $true, $true)

        if ($sourceImage.Width -lt 256 -or $sourceImage.Height -lt 256) {
            throw "O arquivo fonte precisa ter pelo menos 256x256 para gerar um .ico de boa qualidade: $SourcePng"
        }

        $frames = New-Object System.Collections.Generic.List[object]

        foreach ($size in $IconSizes) {
            $bitmap = New-Object System.Drawing.Bitmap($size, $size, [System.Drawing.Imaging.PixelFormat]::Format32bppArgb)
            $graphics = [System.Drawing.Graphics]::FromImage($bitmap)

            try {
                $graphics.Clear([System.Drawing.Color]::Transparent)
                $graphics.CompositingQuality = [System.Drawing.Drawing2D.CompositingQuality]::HighQuality
                $graphics.InterpolationMode = [System.Drawing.Drawing2D.InterpolationMode]::HighQualityBicubic
                $graphics.PixelOffsetMode = [System.Drawing.Drawing2D.PixelOffsetMode]::HighQuality
                $graphics.SmoothingMode = [System.Drawing.Drawing2D.SmoothingMode]::HighQuality

                $destRect = New-Object System.Drawing.Rectangle(0, 0, $size, $size)
                $graphics.DrawImage($sourceImage, $destRect, 0, 0, $sourceImage.Width, $sourceImage.Height, [System.Drawing.GraphicsUnit]::Pixel)

                $pngStream = New-Object System.IO.MemoryStream
                try {
                    $bitmap.Save($pngStream, [System.Drawing.Imaging.ImageFormat]::Png)
                    $frames.Add([pscustomobject]@{
                        Size = $size
                        Bytes = $pngStream.ToArray()
                    }) | Out-Null
                }
                finally {
                    $pngStream.Dispose()
                }
            }
            finally {
                $graphics.Dispose()
                $bitmap.Dispose()
            }
        }

        $fileStream = [System.IO.File]::Open($TargetIco, [System.IO.FileMode]::Create, [System.IO.FileAccess]::Write, [System.IO.FileShare]::None)
        $writer = New-Object System.IO.BinaryWriter($fileStream)

        try {
            $writer.Write([UInt16]0)
            $writer.Write([UInt16]1)
            $writer.Write([UInt16]$frames.Count)

            $offset = 6 + ($frames.Count * 16)
            foreach ($frame in $frames) {
                $dimByte = if ($frame.Size -ge 256) { [byte]0 } else { [byte]$frame.Size }
                $writer.Write($dimByte)
                $writer.Write($dimByte)
                $writer.Write([byte]0)
                $writer.Write([byte]0)
                $writer.Write([UInt16]1)
                $writer.Write([UInt16]32)
                $writer.Write([UInt32]$frame.Bytes.Length)
                $writer.Write([UInt32]$offset)
                $offset += $frame.Bytes.Length
            }

            foreach ($frame in $frames) {
                $writer.Write($frame.Bytes)
            }
        }
        finally {
            $writer.Dispose()
            $fileStream.Dispose()
        }
    }
    finally {
        if ($null -ne $sourceImage) {
            $sourceImage.Dispose()
        }
        if ($null -ne $sourceStream) {
            $sourceStream.Dispose()
        }
    }
}

$srcRoot = Join-Path $ProjectRoot "src"
$buildDir = Join-Path $srcRoot "build"
$windowsIconDir = Join-Path $buildDir "windows"
$iconSizes = @(256, 128, 64, 48, 32, 24, 16)

# Gera um PNG 32x32 (formato recomendado pelo Wails v3 para o systray no Windows).
function Convert-PngToTrayPng {
    param(
        [Parameter(Mandatory = $true)]
        [string]$SourcePng,
        [Parameter(Mandatory = $true)]
        [string]$TargetPng
    )

    $sourceImage = $null
    $bitmap = $null
    $graphics = $null

    try {
        $sourceImage = [System.Drawing.Image]::FromFile($SourcePng)
        $bitmap = New-Object System.Drawing.Bitmap(32, 32, [System.Drawing.Imaging.PixelFormat]::Format32bppArgb)
        $graphics = [System.Drawing.Graphics]::FromImage($bitmap)
        $graphics.Clear([System.Drawing.Color]::Transparent)
        $graphics.CompositingQuality = [System.Drawing.Drawing2D.CompositingQuality]::HighQuality
        $graphics.InterpolationMode = [System.Drawing.Drawing2D.InterpolationMode]::HighQualityBicubic
        $graphics.PixelOffsetMode = [System.Drawing.Drawing2D.PixelOffsetMode]::HighQuality
        $graphics.SmoothingMode = [System.Drawing.Drawing2D.SmoothingMode]::HighQuality
        $graphics.DrawImage($sourceImage, 0, 0, 32, 32)
        $bitmap.Save($TargetPng, [System.Drawing.Imaging.ImageFormat]::Png)
    }
    finally {
        if ($graphics) { $graphics.Dispose() }
        if ($bitmap) { $bitmap.Dispose() }
        if ($sourceImage) { $sourceImage.Dispose() }
    }
}

$iconSpecs = @(
    @{ Source = (Join-Path $buildDir "appicon.png"); Target = (Join-Path $windowsIconDir "icon.ico"); Label = "normal" },
    @{ Source = (Join-Path $buildDir "provisionig.png"); Target = (Join-Path $windowsIconDir "provisionig.ico"); Label = "provisioning" },
    @{ Source = (Join-Path $buildDir "offline.png"); Target = (Join-Path $windowsIconDir "offline.ico"); Label = "offline" }
)

# PNGs 32x32 para o systray (embedados via tray_embed.go).
$trayPngSpecs = @(
    @{ Source = (Join-Path $buildDir "appicon.png"); Target = (Join-Path $windowsIconDir "tray_normal.png"); Label = "tray-normal" },
    @{ Source = (Join-Path $buildDir "provisionig.png"); Target = (Join-Path $windowsIconDir "tray_provisioning.png"); Label = "tray-provisioning" },
    @{ Source = (Join-Path $buildDir "offline.png"); Target = (Join-Path $windowsIconDir "tray_offline.png"); Label = "tray-offline" }
)

if (-not (Test-Path $windowsIconDir)) {
    New-Item -ItemType Directory -Path $windowsIconDir | Out-Null
}

foreach ($icon in $iconSpecs) {
    if (-not (Test-Path $icon.Source)) {
        throw "Arquivo fonte do icone nao encontrado: $($icon.Source)"
    }

    Convert-PngToIco -SourcePng $icon.Source -TargetIco $icon.Target -IconSizes $iconSizes
    Write-Output "Icone $($icon.Label) sincronizado a partir de: $($icon.Source)"
    Write-Output "ICO gerado em: $($icon.Target)"
}

foreach ($tray in $trayPngSpecs) {
    if (-not (Test-Path $tray.Source)) {
        throw "Arquivo fonte do icone nao encontrado: $($tray.Source)"
    }

    Convert-PngToTrayPng -SourcePng $tray.Source -TargetPng $tray.Target
    Write-Output "Icone $($tray.Label) gerado em: $($tray.Target)"
}