package api

import (
	"log"
	"net/http"
	"os"

	"github.com/gorilla/mux"
)

const dirPerm = 0o755

// @title Webring API
// @version 1.0
// @description API for the webring
// @host localhost:8000
// @contact.url mailto://sanspie@akarpov.ru
// @BasePath /

func RegisterSwaggerHandlers(r *mux.Router) {
	ensureDocsDirectory()

	// Register specific JSON endpoint BEFORE the PathPrefix handler
	r.HandleFunc("/docs/swagger.json", swaggerJSONHandler).Methods("GET")
	r.PathPrefix("/docs/").Handler(http.StripPrefix("/docs/", http.FileServer(http.Dir("./docs/"))))
}

func ensureDocsDirectory() {
	docsDir := "docs"
	if err := os.MkdirAll(docsDir, dirPerm); err != nil {
		log.Printf("Warning: Could not create docs directory: %v", err)
	}
}

func swaggerJSONHandler(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if _, err := w.Write([]byte(swaggerJSON)); err != nil {
		log.Printf("Error writing swagger JSON: %v", err)
	}
}

const swaggerJSON = `{
  "swagger": "2.0",
  "info": {
    "title": "Webring API",
    "description": "Public API for navigating the webring",
    "version": "1.0"
  },
  "host": "webring.otomir23.me",
  "basePath": "/",
  "schemes": ["https"],
  "consumes": ["application/json"],
  "produces": ["application/json"],
  "paths": {
    "/sites": {
      "get": {
        "summary": "List all active sites",
        "description": "Returns a list of all sites that are currently responding (is_up = true)",
        "tags": ["Sites"],
        "responses": {
          "200": {
            "description": "List of active sites",
            "schema": {
              "type": "array",
              "items": {
                "$ref": "#/definitions/PublicSite"
              }
            }
          },
          "500": {
            "description": "Internal server error"
          }
        }
      }
    },
    "/{slug}": {
      "get": {
        "summary": "Redirect to site",
        "description": "Redirects to the URL of the specified site",
        "tags": ["Navigation"],
        "parameters": [
          {
            "name": "slug",
            "in": "path",
            "required": true,
            "type": "string",
            "description": "The unique slug identifier for the site"
          }
        ],
        "responses": {
          "302": {
            "description": "Redirect to site URL"
          },
          "404": {
            "description": "Site not found"
          }
        }
      }
    },
    "/{slug}/data": {
      "get": {
        "summary": "Get site data with navigation",
        "description": "Returns the current site along with previous and next sites in the ring",
        "tags": ["Navigation"],
        "parameters": [
          {
            "name": "slug",
            "in": "path",
            "required": true,
            "type": "string",
            "description": "The unique slug identifier for the site"
          }
        ],
        "responses": {
          "200": {
            "description": "Site data with navigation",
            "schema": {
              "$ref": "#/definitions/SiteData"
            }
          },
          "404": {
            "description": "Site not found"
          }
        }
      }
    },
    "/{slug}/next": {
      "get": {
        "summary": "Redirect to next site",
        "description": "Redirects to the next site in the webring based on display order",
        "tags": ["Navigation"],
        "parameters": [
          {
            "name": "slug",
            "in": "path",
            "required": true,
            "type": "string",
            "description": "The unique slug identifier for the current site"
          }
        ],
        "responses": {
          "302": {
            "description": "Redirect to next site URL"
          },
          "404": {
            "description": "Site not found"
          }
        }
      }
    },
    "/{slug}/next/data": {
      "get": {
        "summary": "Get next site data",
        "description": "Returns data for the next site in the webring based on display order",
        "tags": ["Navigation"],
        "parameters": [
          {
            "name": "slug",
            "in": "path",
            "required": true,
            "type": "string",
            "description": "The unique slug identifier for the current site"
          }
        ],
        "responses": {
          "200": {
            "description": "Next site data",
            "schema": {
              "type": "object",
              "properties": {
                "next": {
                  "$ref": "#/definitions/PublicSite"
                }
              }
            }
          },
          "404": {
            "description": "Site not found"
          }
        }
      }
    },
    "/{slug}/prev": {
      "get": {
        "summary": "Redirect to previous site",
        "description": "Redirects to the previous site in the webring based on display order",
        "tags": ["Navigation"],
        "parameters": [
          {
            "name": "slug",
            "in": "path",
            "required": true,
            "type": "string",
            "description": "The unique slug identifier for the current site"
          }
        ],
        "responses": {
          "302": {
            "description": "Redirect to previous site URL"
          },
          "404": {
            "description": "Site not found"
          }
        }
      }
    },
    "/{slug}/prev/data": {
      "get": {
        "summary": "Get previous site data",
        "description": "Returns data for the previous site in the webring based on display order",
        "tags": ["Navigation"],
        "parameters": [
          {
            "name": "slug",
            "in": "path",
            "required": true,
            "type": "string",
            "description": "The unique slug identifier for the current site"
          }
        ],
        "responses": {
          "200": {
            "description": "Previous site data",
            "schema": {
              "type": "object",
              "properties": {
                "previous": {
                  "$ref": "#/definitions/PublicSite"
                }
              }
            }
          },
          "404": {
            "description": "Site not found"
          }
        }
      }
    },
    "/{slug}/random": {
      "get": {
        "summary": "Redirect to random site",
        "description": "Redirects to a random site in the webring (excluding the current site)",
        "tags": ["Navigation"],
        "parameters": [
          {
            "name": "slug",
            "in": "path",
            "required": true,
            "type": "string",
            "description": "The unique slug identifier for the current site"
          }
        ],
        "responses": {
          "302": {
            "description": "Redirect to random site URL"
          },
          "404": {
            "description": "No available sites found"
          }
        }
      }
    },
    "/{slug}/random/data": {
      "get": {
        "summary": "Get random site data",
        "description": "Returns data for a random site in the webring (excluding the current site)",
        "tags": ["Navigation"],
        "parameters": [
          {
            "name": "slug",
            "in": "path",
            "required": true,
            "type": "string",
            "description": "The unique slug identifier for the current site"
          }
        ],
        "responses": {
          "200": {
            "description": "Random site data",
            "schema": {
              "type": "object",
              "properties": {
                "random": {
                  "$ref": "#/definitions/PublicSite"
                }
              }
            }
          },
          "404": {
            "description": "No available sites found"
          }
        }
      }
    }
  },
  "definitions": {
    "PublicSite": {
      "type": "object",
      "properties": {
        "id": {
          "type": "integer",
          "description": "Unique identifier for the site"
        },
        "slug": {
          "type": "string",
          "description": "URL-friendly unique identifier"
        },
        "name": {
          "type": "string",
          "description": "Display name of the site"
        },
        "url": {
          "type": "string",
          "description": "Full URL of the site"
        },
        "favicon": {
          "type": "string",
          "description": "Path to the site's favicon (nullable)"
        }
      },
      "required": ["id", "slug", "name", "url"]
    },
    "SiteData": {
      "type": "object",
      "properties": {
        "prev": {
          "$ref": "#/definitions/PublicSite",
          "description": "Previous site in the webring"
        },
        "curr": {
          "$ref": "#/definitions/PublicSite",
          "description": "Current site"
        },
        "next": {
          "$ref": "#/definitions/PublicSite",
          "description": "Next site in the webring"
        }
      },
      "required": ["prev", "curr", "next"]
    }
  }
}`
