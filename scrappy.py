#!/usr/bin/env python3.11

import os
import sqlite3
import fire
from newspaper import Article
import nltk

# Download the punkt tokenizer for sentence splitting
nltk.download("punkt", quiet=True)


def setup_database():
    db_path = os.path.join(os.path.expanduser("~"), ".scrappy", "scrappy_notes.db")
    os.makedirs(os.path.dirname(db_path), exist_ok=True)
    conn = sqlite3.connect(db_path)
    cursor = conn.cursor()
    cursor.execute(
        """CREATE TABLE IF NOT EXISTS notes (
                        url TEXT PRIMARY KEY,
                        content TEXT
                    )"""
    )
    conn.commit()
    return conn, cursor


def extract_article_content(url):
    article = Article(url)
    article.download()
    article.parse()
    return article.text


def scrape(url):
    conn, cursor = setup_database()

    # Check if entry already exists
    cursor.execute("SELECT COUNT(*) FROM notes WHERE url = ?", (url,))
    count = cursor.fetchone()[0]

    if count > 0:
        response = (
            input(
                "An entry for this URL already exists. Do you want to overwrite it? (y/n): "
            )
            .lower()
            .strip()
        )
        if response != "y":
            print("Operation cancelled.")
            conn.close()
            return

    content = extract_article_content(url)

    # Save or update content in database
    cursor.execute(
        "INSERT OR REPLACE INTO notes (url, content) VALUES (?, ?)", (url, content)
    )
    conn.commit()
    conn.close()

    print("Content saved successfully.")


if __name__ == "__main__":
    fire.Fire(scrape)
