# How Do I?

Simple CLI tool that targets LLM APIs to figure how to do stuff quickly! Supports Anthropic, Gemini, and OpenAI models.

## Install

Download:

You can download the latest release from the releases tab.

Export the key for the model(s) you want to use:

```sh
`ANTHROPIC_API_KEY`.
`GEMINI_API_KEY`.
`OPENAI_API_KEY`.
```

## Usage

The program takes in an array of arguments. These can be images, text files, or a plain string. If you have context you'll pass those in first and then type your question at the end.

```sh
howdoi context1.js context2.html "this is my question"
howdoi animal.png "what is the animal in the image"
```
## Extra

Content is written to stdout so you can pipe the content to a file.

```sh
λ ~/code/howdoi: howdoi "add a line break to a markdown file. the line break should be visible, like a clear separation of two sections" > foo.txt
2024/04/02 15:34:47 Usage: Input Tokens: 30, Output Tokens: 75, Total Cost: $0.000101
λ ~/code/howdoi: cat foo.txt
───────┬─────────────────────────────────────────────────────────────────────────────────
       │ File: foo.txt
───────┼─────────────────────────────────────────────────────────────────────────────────
   1   │ To add a visible line break in a Markdown file, you can use the HTML `<br>` tag
       │ or three consecutive asterisks `***`.
   2   │
   3   │ Here's an example:
   4   │
   5   │ Section 1
   6   │
   7   │ ***
   8   │
   9   │ Section 2
  10   │
  11   │ This will create a clear separation between the two sections, with the line brea
       │ k being visible in the rendered Markdown.
  12   │
```
